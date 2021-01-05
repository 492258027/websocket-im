package main

import (
	"encoding/base64"
	"flag"
	"github.com/abiosoft/ishell"
	"github.com/golang/protobuf/proto"
	"github.com/gorilla/websocket"
	"log"
	"math/rand"
	"net/url"
	"time"
	pb "websocket-im/pb"
)

var addr = flag.String("addr", "192.168.73.3:5340", "http service address")

func main() {
	flag.Parse()
	log.SetFlags(0)

	// create new shell.
	// by default, new shell includes 'exit', 'help' and 'clear' commands.
	shell := ishell.New()

	//登录
	shell.AddCmd(&ishell.Cmd{
		Name: "login",
		Help: "simulate a login",
		Func: func(c *ishell.Context) {
			c.ShowPrompt(false)
			defer c.ShowPrompt(true)

			c.Println("Let's simulate login")

			// prompt for input
			c.Print("UserID: ")
			userid := c.ReadLine()
			c.Print("device type(win/ios): ")
			devicetype := c.ReadLine()
			//	c.Print("device id: ")
			//	deviceid := c.ReadLine()
			//	c.Print("auto login(true/false): ")
			//	autologin := c.ReadLine()
			deviceid := randomString(20, defaultLetters)
			autologin := "false"
			c.Println("Your inputs were", userid)
			login(userid, devicetype, deviceid, autologin, c)
		},
	})

	//登出
	shell.AddCmd(&ishell.Cmd{
		Name: "logout",
		Help: "simulate a logout",
		Func: func(c *ishell.Context) {
			c.ShowPrompt(false)
			defer c.ShowPrompt(true)

			c.Println("Let's simulate logout")
			logout(conn, c)
		},
	})

	//私聊
	shell.AddCmd(&ishell.Cmd{
		Name: "prichat",
		Help: "simulate a prichat",
		Func: func(c *ishell.Context) {
			c.ShowPrompt(false)
			defer c.ShowPrompt(true)

			c.Println("Let's simulate prichat")

			// prompt for input
			c.Print("fromid: ")
			fromid := c.ReadLine()
			c.Print("toid: ")
			toid := c.ReadLine()
			c.Print("txt: ")
			txt := c.ReadLine()

			// do something with username and password
			c.Println("Your inputs were", toid, "and", txt)
			priChat(fromid, toid, txt, conn, c)
		},
	})

	// run shell
	shell.Run()
}

//接收消息routine， 需要区分： 端上主动发送的，接收到的是服务端回的ack。 服务端下推的，接收到信息后需要回一个ack，ack中是切片

func recvHandle(conn *websocket.Conn) {

	for {
	HERE:
		//收消息
		_, recvMsg, err := conn.ReadMessage()
		if err != nil {
			log.Println("read:", err)
			return
		}
		//log.Printf("recv: %s", recvMsg)

		//解消息
		msgbatch := pb.ServerSendMsgs{}
		err = proto.Unmarshal(recvMsg, &msgbatch)
		//err = msgbatch.Unmarshal(recvMsg)
		if err != nil {
			log.Println("unmarshal failure!")
			continue
		}

		if msgbatch.MsgContent == nil {
			log.Println("MsgContent nil!")
			continue
		}

		for _, msg := range msgbatch.MsgContent {
			if msg.MsgType == pb.MsgTypeEnum_ACK {
				//log.Println("recv msg: ", msg.MsgType, msg.ServerMsgId, msg.Ser2CliAckMsg.Code, msg.Ser2CliAckMsg.Des)
				goto HERE
			}

			if msg.MsgType == pb.MsgTypeEnum_HEARTBEAT {
				goto HERE
			}

			//跳出当层循环
			break
		}

		//处理服务端主动下推的消息
		r := make([]int64, 1)
		for _, msg := range msgbatch.MsgContent {
			if msg.MsgType == pb.MsgTypeEnum_PRICHAT {
				r = append(r, msg.ServerMsgId)
				log.Println("[", msg.FromId, "->", msg.ToId, "]", "  msg:", msg.ChatMsg.TxtContent)
			}
		}

		ack := &pb.Client2ServerAckMsgSt{
			ServerMsgId: r,
		}

		sendMsg := &pb.MsgSt{
			MsgType:       pb.MsgTypeEnum_ACK,
			Cli2SerAckMsg: ack,
		}

		data, err := proto.Marshal(sendMsg)
		//data, err := sendMsg.Marshal()
		if err != nil {
			log.Fatal("login marshaling error: ", err)
		}

		err = conn.WriteMessage(websocket.TextMessage, data)
		if err != nil {
			log.Fatal("write:", err)
		}
	}
}

//发送心跳的routine
const Heartbeat = 60

func heartHandle(conn *websocket.Conn) {
	for {
		time.Sleep(Heartbeat * time.Second)

		//发消息
		msg := &pb.MsgSt{MsgType: pb.MsgTypeEnum_HEARTBEAT}

		data, err := proto.Marshal(msg)
		//data, err := msg.Marshal()
		if err != nil {
			log.Fatal("heart marshaling error: ", err)
		}

		err = conn.WriteMessage(websocket.TextMessage, data)
		if err != nil {
			log.Fatal("write:", err)
		}
	}
}

//登录并连接websocket
var conn *websocket.Conn = nil

func login(userid, devicetype, deviceid, autoLogin string, sh *ishell.Context) {
	//取设备类型
	var dtype pb.DeviceTypeEnum
	if devicetype == "win" {
		dtype = pb.DeviceTypeEnum_WIN
	} else if devicetype == "ios" {
		dtype = pb.DeviceTypeEnum_IOS
	} else {
		sh.Println("deviceType error")
		return
	}

	//取设备自动登录状态
	var auto bool
	if autoLogin == "true" {
		auto = true
	} else if autoLogin == "false" {
		auto = false
	} else {
		sh.Println("auto login error")
		return
	}

	login := &pb.LoginMsgSt{
		Ver:         "2.0",
		Device:      dtype,
		DeviceModel: "thinkpad",
		//DeviceId:    randomString(20, defaultLetters),
		DeviceId: deviceid,
		Auto:     auto,
	}

	data, err := proto.Marshal(login)
	//data, err := login.Marshal()
	if err != nil {
		sh.Println("login marshaling error: ", err)
		return
	}

	rawquery := "userid=" + userid + "&login=" + base64.URLEncoding.EncodeToString(data) + "&token=" + "token"

	u := url.URL{Scheme: "ws", Host: *addr, Path: "/IM", RawQuery: rawquery}
	log.Printf("connecting to %s", u.String())

	conn, _, err = websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		sh.Println("dial:", err)
		return
	}

	//logout时再断链
	//defer conn.Close()

	//启动接收消息routine
	go recvHandle(conn)

	//启动心跳routine
	go heartHandle(conn)
}

//发退出消息
func logout(conn *websocket.Conn, sh *ishell.Context) {
	if conn == nil {
		sh.Println("please login first!")
		return
	}

	//发消息
	msg := &pb.MsgSt{MsgType: pb.MsgTypeEnum_LOGOUT}

	data, err := proto.Marshal(msg)
	//data, err := msg.Marshal()
	if err != nil {
		log.Fatal("logout marshaling error: ", err)
	}

	err = conn.WriteMessage(websocket.TextMessage, data)
	if err != nil {
		log.Fatal("write:", err)
	}

	conn.Close()

	sh.Println("send logout msg success!")
}

//发送私聊消息
func priChat(fromid, toid, txt string, conn *websocket.Conn, sh *ishell.Context) {
	if conn == nil {
		sh.Println("please login first!")
		return
	}

	//发消息
	chat := &pb.ChatMsgSt{
		ContentType: pb.ContentTypeEnum_TXT,
		TxtContent:  txt,
	}

	msg := &pb.MsgSt{
		FromId:      fromid,
		ToId:        toid,
		ClientMsgId: time.Now().UnixNano(),
		Device:      pb.DeviceTypeEnum_WIN,
		MsgType:     pb.MsgTypeEnum_PRICHAT,
		ChatMsg:     chat,
	}

	data, err := proto.Marshal(msg)
	//data, err := msg.Marshal()
	if err != nil {
		log.Println("marshaling error: ", err)
	}

	err = conn.WriteMessage(websocket.TextMessage, data)
	if err != nil {
		log.Fatal("write:", err)
	}

	sh.Println("send prichat msg success!")
}

// RandomString returns a random string with a fixed length
var defaultLetters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

func randomString(n int, allowedChars ...[]rune) string {
	var letters []rune

	if len(allowedChars) == 0 {
		letters = defaultLetters
	} else {
		letters = allowedChars[0]
	}

	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}

	return string(b)
}
