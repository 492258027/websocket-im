package millipede

import (
	"encoding/base64"
	"errors"
	"github.com/golang/protobuf/proto"
	"github.com/gorilla/websocket"
	"net/http"
	"net/url"
	"time"
	pb "websocket-im/pb"
	"websocket-im/util/bootstrap"
	"websocket-im/util/log"
	mq "websocket-im/util/rabbitmq"
	"websocket-im/util/snowflake"
)

//当前任务：删除过期消息, queue服务分配millipede
//以后再说：群操作相关，黑名单限流，敏感词，离线推送

//记录消息到接收者消息队列（redis），更换全局消息id， 然后给发送方回ack
//从map表中查找接收端的ws, 如果找到直接发（接收者和发送者在同一个millipede）
//从redis中查找接收端是否在线及所在的millipede, 如果找到优先通过grpc发新消息通知，如果grpc失败则把新消息通知写入到消息队列
//如接收者不在线则通过grpc调离线推送服务
//无论是客户端主动发送给服务端的消息， 还是服务端推送给客户端的消息， 收到者均需回复Ack。
//向客户端推送消息重试三次，三次后还没收到客户端的Ack就关闭链接
//客户端每隔五秒主动发心跳包，服务端接到心跳包后复位心跳定时器并回复Ack，服务端超过五秒钟没接到消息就关闭链接
//消息来源： 1 ws。 2 mq。 3 grpc。

const MaxCltMsgId = 20           //端上消息排重数组容量
const Heartbeat = 180            //心跳定时器阈值
const MaxAckTimes = 3            //重试次数，超过次数未回ack则断链
const Ackbeat = 1                //端上回ack的时间阈值
const MsgBatchNum = 20           //批量发送消息容量
const GrpcTimeout = 1            //grpc 超时时间阈值
const ForwardMsgTimeout = 10     //本地消息转发到writerCh超时阈值， 毫秒
const IsAutoAckMq = false        //消费者读取mq是否自动回Ack
var MaxDaysOfflineMsg = int64(7) //清理未读消息时间阈值，单位为天， 可以通过管理端改变

func ReadMsg(w http.ResponseWriter, r *http.Request) {
	//解析http相关参数。
	userId := ""
	login := pb.LoginMsgSt{}
	token := ""

	//解userid
	params, _ := url.ParseQuery(r.URL.RawQuery)
	if v, ok := params["userid"]; ok {
		userId = v[0]
	}

	//解login结构
	if v, ok := params["login"]; ok {
		log.Logrus.Debugln(v[0])
		//解base64
		uDec, err := base64.URLEncoding.DecodeString(v[0])
		if err != nil {
			log.Logrus.Errorln(err)
			return
		}
		//解protobuf
		if err = proto.Unmarshal(uDec, &login); err != nil {
			//if err = login.Unmarshal(uDec); err != nil {
			log.Logrus.Errorln(err)
			return
		}
	}

	if v, ok := params["token"]; ok {
		token = v[0]
	}

	//校验并打印登录信息
	log.Logrus.Debugln("userid:", userId, "DeviceType:", login.Device, "deviceModel:", login.DeviceModel, "DeviceId:", login.DeviceId, "version:", login.Ver, "AutoLogin:", login.Auto, "Atoken:", token)

	//鉴权失败
	if err := checkToken_grpc(token); err != nil {
		http.Error(w, http.StatusText(401), http.StatusUnauthorized)
		log.Logrus.Errorln("token invalid")
		return
	}

	//声明chan， 注意容量有影响
	writerCh := make(chan pb.MsgSt, 1)
	defer func() {
		log.Logrus.Infoln(writerCh, " readRoutine close chan!")
		close(writerCh)
	}()

	//生成map中用户信息结构
	user := &UserSt{
		WriterCh: writerCh,
		UserRedis: pb.UserRedisSt{
			Userstat:    pb.UserStatEnum_ONLINE,
			Version:     login.Ver,
			DeviceType:  login.Device,
			DeviceModel: login.DeviceModel,
			DeviceId:    login.DeviceId,
			MillipedeId: bootstrap.ConsulConfig.InstanceId,
		},
	}

	AutoLogin := login.Auto
	//登录踢人处理逻辑 //
	if code := LoginCheck(userId, user, AutoLogin); code == pb.AckCodeEnum_FORBIDDEN {
		http.Error(w, http.StatusText(403), http.StatusForbidden)
		log.Logrus.Errorln("other device in use")
		return
	}

	//升级到websocket处理方式
	conn, err := (&websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		// 允许所有CORS跨域请求
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}).Upgrade(w, r, nil)
	if err != nil {
		log.Logrus.Errorln("upgrade error:", err)
		http.NotFound(w, r)
		return
	}
	defer func() {
		log.Logrus.Infoln(writerCh, " readRoutine close conn!")
		conn.Close()
	}()

	//设置读取消息大小上线
	conn.SetReadLimit(10240)

	//启动消息处理routine
	go handleMsg(userId, conn, user)

	//开始接受消息
	for {
		//从client端接收消息, 第一返回值是ws消息类型
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Logrus.Errorln("read:", err)
			break
		}

		msg := pb.MsgSt{}
		err = proto.Unmarshal(message, &msg)
		//err = msg.Unmarshal(message)
		if err != nil {
			log.Logrus.Errorln("ReadMsg: unmarshaling error: ", err)
		}

		writerCh <- msg
	}
}

func handleMsg(userid string, conn *websocket.Conn, user *UserSt) {

	//心跳定时器，收到所有client来的消息都会重置定时器
	timerHeartbeat := time.NewTimer(time.Duration(Heartbeat) * time.Second)
	//向client发送消息之后，等待client回ack的定时器，超时3次认为client离线，
	timerAck := time.NewTimer(time.Duration(Ackbeat) * time.Second)
	timerStop(timerAck)

	//消息排重记录,用户登录从redis取,用户离线写入redis
	var index int = 0
	var msgids [MaxCltMsgId]MsgidSt

	var kickout int = 0

	//客户端是否回了ack
	var ackFlag bool = true
	var ackTimes int = 0

	var writerCh chan pb.MsgSt = user.WriterCh
	deviceId := user.UserRedis.DeviceId
	useridmap := ""

	var sendByte []byte

	//这个匿名函数负责接error，保证应用程序不崩溃。
	defer func() {
		if err := recover(); err != nil {
			log.Logrus.Errorln(writerCh, useridmap, "handleRoutine error : ", err)
		}
	}()

	//这个匿名函数处理客户端主动退出/错误/断链等
	//如果millipede程序崩溃,redis里用户online的状态会失真,需client重新登录修复
	defer func() {
		switch kickout {
		case 1:
			//被踢掉，踢人者和被踢者在同一个millipede, 不回写redis，不修改map, 什么操作也不做
			log.Logrus.Debugln(writerCh, useridmap, " kickout complete,  same millipede !", conn.RemoteAddr())
		case 2:
			//被踢掉，踢人者和被踢者不在同一个millipede, 不回写redis，在本地map中删除用户。
			DelUseFromMap(useridmap)
			log.Logrus.Debugln(writerCh, useridmap, " kickout complete,  diff millipede !", conn.RemoteAddr())
		default:
			//用户信息写入redis
			AddUserToRedis(useridmap)

			//消息排重记录写入redis
			AddMsgIdToRedis(useridmap, &msgids)

			//从map表中删除用户
			DelUseFromMap(useridmap)
			log.Logrus.Debugln(writerCh, useridmap, " activequit/disconnection complete", conn.RemoteAddr())
		}

		//关闭用户链接
		conn.Close()
		log.Logrus.Errorln(writerCh, useridmap, "handleroutine closed!", conn.RemoteAddr())
	}()

	//判断客户端类型, 并生产新的userid_m 或者 userid_pc
	var err error
	useridmap, err = reviseUserid(userid, user.UserRedis.DeviceType)
	if err != nil {
		return
	}

	//取排重消息ID
	GetMsgIdFromRedis(useridmap, &msgids)
	//for _, v := range msgids {
	//	log.Logrus.Debugln("读出: ", v.CliMsgid, v.SvrMsgid)
	//}

	//发个新消息通知， 触发消息流程
	msgNew := pb.MsgSt{
		MsgType: pb.MsgTypeEnum_NEWMSG,
	}
	writerCh <- msgNew

	for {
		select {
		case <-timerHeartbeat.C:
			log.Logrus.Debugln(writerCh, useridmap, "handleRoutine: link timeout!")
			ModifyMap(useridmap, pb.UserStatEnum_OFFLINE)
			return
		case <-timerAck.C:
			//因为开始时候关闭了ack定时器， 这里不用加ackFlag
			if ackTimes < MaxAckTimes {
				ackTimes++
				//进来必是因为定时器超时，而且读取了channel， 可以直接reset
				timerAck.Reset(time.Duration(Ackbeat) * time.Second)
				log.Logrus.Errorln(writerCh, useridmap, "msg ACK timeout!", deviceId, ackTimes)
				//每次ack超时均重发当前帧
				err := conn.WriteMessage(websocket.TextMessage, sendByte)
				if err != nil {
					log.Logrus.Errorln("<---", writerCh, useridmap, "connWrite error", err)
				}
			} else {
				//超时n次，没收到message ack，直接断链,强制离线
				log.Logrus.Errorln(writerCh, useridmap, "ACK timeout and kill link", deviceId, ackTimes+1)
				ModifyMap(useridmap, pb.UserStatEnum_OFFLINE)
				return
			}

		case msg, ok := <-writerCh:
			log.Logrus.Debugln("--->", writerCh, useridmap, "handleRoutine Receive msg =", msg.String(), ok, deviceId)

			//从Channel接收一个消息，如果channel关闭了,ok返回值为false
			if ok {
				switch msg.MsgType {

				//客户端发送的退出登录,不入消息队列
				case pb.MsgTypeEnum_LOGOUT:
					//主动退出登录,回ACK
					Ser2CltAck(conn, useridmap, msg, pb.AckCodeEnum_OK, "")
					ModifyMap(useridmap, pb.UserStatEnum_ACTIVEQUIT)

					log.Logrus.Infoln(writerCh, useridmap, "退出登录:", deviceId)

					//各端主动退出登录,不相互影响
					return

				//客户端发送的心跳包, 复位心跳定时器，不入消息队列
				case pb.MsgTypeEnum_HEARTBEAT:
					//回ACK
					Ser2CltHeartAck(conn, useridmap)
					//只要收到client的报文就复位心跳定时器
					timerReset(timerHeartbeat, Heartbeat)

				//客户端发送的切入前台, 修改用户状态, 写入redis, 不入消息队列
				case pb.MsgTypeEnum_ENTERFOREGROUND:
					//回ACK
					Ser2CltAck(conn, useridmap, msg, pb.AckCodeEnum_OK, "")

					//修改用户状态为切入前台
					ModifyMap(useridmap, pb.UserStatEnum_ONLINE)

					//切入前台状态写入redis
					AddUserToRedis(useridmap)

					//只要收到client的报文就复位心跳定时器
					timerReset(timerHeartbeat, Heartbeat)

				//客户端发送的切入后台, 修改用户状态, 写入redis, 不入消息队列。 未读消息数(delay， 服务端保存给谁用)
				case pb.MsgTypeEnum_ENTERBACKGROUND:
					//修改用户状态为切入后台
					if msg.ClientSleepMsg != nil {
						ModifyMap_Sleep(useridmap, pb.UserStatEnum_BACKSTAGE, msg.ClientSleepMsg.UnReadMsgNum)
						Ser2CltAck(conn, useridmap, msg, pb.AckCodeEnum_OK, "")

						//切入后台状态写入redis,包含未读消息数
						AddUserToRedis(useridmap)
					} else {
						log.Logrus.Errorln(writerCh, useridmap, "ClientSleepMsg nil!", deviceId, msg.String())
						Ser2CltAck(conn, useridmap, msg, pb.AckCodeEnum_MSGFIELD_LACK, "ClientSleepMsg nil!")
					}

					//只要收到client的报文就复位心跳定时器
					timerReset(timerHeartbeat, Heartbeat)

				//客户端发送的私聊消息/群聊消息, 1 消息排重 2入接收者消息队列， 3回ack，4下推给接收方(不同的millipede走grpc，失败走mq)
				case pb.MsgTypeEnum_PRICHAT, pb.MsgTypeEnum_GROUPCHAT:
					//此处更改msg的svrmsgid, 服务端收到消息并生成对应的svrmsgid。客户端重发clientmsgid保持不变，服务端还用与之对应的svrmsgid。
					svrmsgid := snowflake.GetSnowflakeId()

					//补充消息缺省字段
					msg.FromId = userid
					msg.Device = user.UserRedis.DeviceType

					//消息体为空,不处理
					if msg.ChatMsg == nil || msg.ClientMsgId == 0 || msg.ToId == "" || msg.FromId == "" {
						log.Logrus.Errorln(writerCh, useridmap, "message error!", deviceId, msg.String())
						Ser2CltAck(conn, useridmap, msg, pb.AckCodeEnum_MSGFIELD_LACK, "message error!")
						continue
					}

					//if msg.ChatMsg.TxtContent != "" {
					//	log.Logrus.Debugln("--->", writerCh, useridmap, "to", msg.ToId, "TxtContent =", msg.ChatMsg.TxtContent)
					//}

					//检查fromid是否在群里
					//if msg.MsgType == pb.MsgTypeEnum_GROUPCHAT {
					//	if userIsInGroup, _ := GetUseridsFromGroup(userid, msg.ToId); !userIsInGroup {
					//		log.Logrus.Errorln(writerCh, useridmap, " is not in group!", deviceId, msg.String())
					//		Ser2CltAck(conn, useridmap, msg, pb.AckCodeEnum_FORBIDDEN, "The user is not in group!")
					//		continue
					//	}
					//}

					//消息排重
					var svrmsgidold int64
					//重发的消息， 服务端已经处理过，无需重复处理
					if judgeDuplication(msg.ClientMsgId, svrmsgid, &msgids, &svrmsgidold, &index) {
						if svrmsgidold != 0 {
							svrmsgid = svrmsgidold
						}
						log.Logrus.Infoln(writerCh, useridmap, "repeat msg!", deviceId, msg.String())
						Ser2CltMsgAck(conn, useridmap, msg, pb.AckCodeEnum_OK, svrmsgid, "")
					} else { //新消息
						msg.ServerMsgId = svrmsgid
						dispatchMsg(conn, &msg, useridmap, &msgids)
					}

					//只要收到client的报文就复位心跳定时器
					timerReset(timerHeartbeat, Heartbeat)

				//客户端作为消息接收者，接收到消息后向服务端回Ack，服务端收到客户端回的Ack, 删除redis中保存的对应消息，不入消息队列
				case pb.MsgTypeEnum_ACK:
					//判断msg.Cli2SerAckMsg,否则会指向非法地址,导致程序崩溃!!!
					if msg.Cli2SerAckMsg == nil {
						log.Logrus.Errorln(writerCh, useridmap, "Cli2SerAckMsg nil!", deviceId, msg.String())
						Ser2CltAck(conn, useridmap, msg, pb.AckCodeEnum_MSGFIELD_LACK, "Cli2SerAckMsg nil!")
						continue
					}

					//从接收者队列中删除消息记录
					DelMsgFromRedis(useridmap, msg.Cli2SerAckMsg)

					//停止ack定时器
					timerStop(timerAck)
					ackFlag = true

					//只要收到client的报文就复位心跳定时器
					timerReset(timerHeartbeat, Heartbeat)

					//从redis接收者队列取第一条消息,操作同下
					fallthrough

				//其他millipede通过grpc/mq转发的新消息通知, 客户端登录成功后也会主动触发一次。
				//从接收队列取消息下推给client, 批量发送。
				case pb.MsgTypeEnum_NEWMSG:
					//登录后需主动触发一次, 后续客户端每次回完ack后触发。
					//grpc或者mq接到新消息通知后也触发
					if ackFlag == true {
						//从redis接收者队列读取消息
						msgs, sucess := GetMsgFromRedis(useridmap)
						if sucess {
							var err error
							sendByte, err := proto.Marshal(msgs)
							//sendByte, err = msgs.Marshal()
							if err != nil {
								log.Logrus.Errorln(writerCh, useridmap, "MsgTypeEnum_NEWMSG marshaling faild", deviceId)
							} else {
								err1 := conn.WriteMessage(websocket.TextMessage, sendByte)
								if err1 != nil {
									log.Logrus.Errorln("<---", writerCh, useridmap, "connWrite error", err1)
								}
								log.Logrus.Debugln("<---", writerCh, useridmap, "NEWMSG num =", len(msgs.MsgContent), deviceId, msgs.String(), "NEWMSG LEN =", len(sendByte))

								//复位定时器
								timerReset(timerAck, Ackbeat)
								ackFlag = false
								ackTimes = 0
							}
						}
					}

				//接收到的踢人消息, 此为被踢者routine, 被踢后需要发通知给端
				//被踢掉者修改redis和踢人者登录修改redis会有竞争条件, 现在采用的方法是被踢掉者直接退出，不修改redis来避免竞争。
				//被踢掉者修改map和踢人者登录修改map会有竞争条件, 如果被踢者和踢人者在同一个millipede，不修改map直接退出， 如不在，则删除本地map表中的信息(被踢者退出时删)
				case pb.MsgTypeEnum_KICKOUT:
					if msg.KickoutMsg != nil {
						if msg.KickoutMsg.KickType == pb.KickTypeEnum_SAMEITEMLOGIN {
							if msg.KickoutMsg.MillipedeId == bootstrap.ConsulConfig.InstanceId {
								//踢人者和被踢者在同一个millipede
								kickout = 1
							} else {
								kickout = 2
							}

							//如果踢人端和被踢端是一个终端，不发踢人消息，否则客户端会掉线
							if deviceId == msg.KickoutMsg.FromDeviceId {
								log.Logrus.Debugln(writerCh, useridmap, "is kicked by same phone!", deviceId, msg.KickoutMsg.KickType)
								return
							}
						} else {
							//被服务器踢掉,状态切换为主动退出登录,会执行DelUseFromMap修改服务器在线用户数量
							ModifyMap(useridmap, pb.UserStatEnum_ACTIVEQUIT)
						}

						//生成转发给client的消息结构
						sendMsgs := &pb.ServerSendMsgs{MsgContent: []*pb.MsgSt{&msg}}

						//转发给客户端,修改用户状态,关闭TCP
						if sendByte, err := proto.Marshal(sendMsgs); err != nil {
							//if sendByte, err := sendMsgs.Marshal(); err != nil {
							log.Logrus.Errorln(writerCh, useridmap, "MsgTypeEnum_KICKOUT marshaling error: ", deviceId, err)
						} else {
							err1 := conn.WriteMessage(websocket.TextMessage, sendByte)
							if err1 != nil {
								log.Logrus.Errorln("<---", writerCh, useridmap, "connWrite error", err1)
							}

							log.Logrus.Infoln(writerCh, useridmap, "send kickout msg to client ok!", deviceId, msg.KickoutMsg.KickType)
							return
						}
					}
				default:
					log.Logrus.Errorln(writerCh, useridmap, "undefined msg type", deviceId)
				}
			} else {
				//断链下线，
				log.Logrus.Errorln(writerCh, useridmap, "writerCh break and kill link")
				ModifyMap(useridmap, pb.UserStatEnum_OFFLINE)
				return
			}
		}
	}
}

//所有IM服务都共用一个exechange，采用direct方式，转发到后端指定的队列
//每个IM服务对应一个queue， queue名字根据注册到consul的服务名和实例名配置
//生产者是无数个handle routine，必须用链接池控制，
//消费者是链接池的一条链接，每个IM服务有一个消费者，用来接收转发过来的消息
func GetDataFromMq() {
	mqPool := mq.MqClient

	if mqPool == nil {
		log.Logrus.Fatal("GetDataFromMq: mqPool point nil!!")
	}

	//从连接池获取连接。 只取一次， 取出来后链接不可用，直接重新创建，不再和链接池交互。
	conn := mqPool.OpenClient()

	defer func() {
		if err := recover(); err != nil {
			log.Logrus.Errorln("defer func, getDataFromMq error : ", err)
			conn.CloseMqConn()
			go GetDataFromMq()
		}
	}()

HERE:
	//取链接成功
	if conn != nil {
		msgCh := conn.Consumer_mq(IsAutoAckMq)
		for {
			select {
			case hint, ok := <-msgCh:
				if ok {
					log.Logrus.Infoln("consume", hint.Body)
					msg := &pb.MsgSt{}
					if err := proto.Unmarshal(hint.Body, msg); err != nil {
						//if err := msg.Unmarshal(hint.Body); err != nil {
						log.Logrus.Errorln("unmarshaling error: ", err, msg.ServerMsgId)
						//解失败了，丢掉本条， 继续从消息队列读消息
						if !IsAutoAckMq {
							//消息处理成功,给mq回Ack，删除队列中的消息
							hint.Ack(false)
						}

						continue
					}

					//转发消息到对应chan
					switch msg.Device {
					case pb.DeviceTypeEnum_WIN, pb.DeviceTypeEnum_IOS:
						if err := ForwardMsgToCh(msg); err == nil {
							log.Logrus.Debugln(msg.ToId, msg.Device, "local forward msg success!")
						}
					case pb.DeviceTypeEnum_ALL:
						msg.Device = pb.DeviceTypeEnum_WIN
						if err := ForwardMsgToCh(msg); err == nil {
							log.Logrus.Debugln(msg.ToId, msg.Device, "local forward msg to pc success!")
						}
						msg.Device = pb.DeviceTypeEnum_IOS
						if err := ForwardMsgToCh(msg); err == nil {
							log.Logrus.Debugln(msg.ToId, msg.Device, "local forward msg to phone success!")
						}
					}

					if !IsAutoAckMq {
						//消息处理成功,给mq回Ack，删除队列中的消息
						hint.Ack(false)
					}
				} else {
					log.Logrus.Errorln("get msg from chan failure! disconnect link and reconnect!")
					conn.CloseMqConn()
					// chan 读取失败， 需要隔一秒尝试创建一次，直到创建链接成功
					for {
						conn = mq.NewMqConn(mqPool.AmqpURI, mqPool.ExchangeName, mqPool.ExchangeType, mqPool.QueueName, mqPool.RoutingKey)
						if conn != nil {
							log.Logrus.Infoln(" create new link ok, goto here begin work!! chan func")
							goto HERE
						} else {
							log.Logrus.Infoln("create new link failure, sleep!! chan func")
							time.Sleep(time.Second)
						}
					}
				}
			}
		}
	}

	//pool 取链接失败，需要隔一秒尝试创建一次，直到创建链接成功
	for {
		log.Logrus.Errorln(" get link from pool failure!! pool func")
		conn = mq.NewMqConn(mqPool.AmqpURI, mqPool.ExchangeName, mqPool.ExchangeType, mqPool.QueueName, mqPool.RoutingKey)
		if conn != nil {
			log.Logrus.Infoln("create new link ok, goto here begin work!! pool func")
			goto HERE
		} else {
			log.Logrus.Infoln("create new link failure, sleep!! pool func")
			time.Sleep(time.Second)
		}
	}
}

//通过mq向其他millipede转发新消息通知
//新消息通知消息中需要提供toid及deviceType。其中toid指定转发到那个用户，deviceType指定转发到那个端
func PutMsgToMq(millipedeId string, msg *pb.MsgSt) error {
	//通过millipedeID组routingKey
	routingKey := "im_" + millipedeId

	data, err := proto.Marshal(msg)
	//data, err := msg.Marshal()
	if err != nil {
		log.Logrus.Errorln(msg.FromId, "marshaling faild")
		return errors.New("marshaling faild")
	}

	return mq.PutDataToMq(mq.MqClient, routingKey, data)
}

//向其他millipede转发新消息通知
//新消息通知消息中需要提供toid及deviceType。其中toid指定转发到那个用户，deviceType指定转发到那个端
// 不需要每次新建连接，可以复用Cli, delay
func ForwardMsgRpc(millipedeId string, msg *pb.MsgSt) error {
	imResp, err := Cli.Forward(context.TODO(), msg)
	if err != nil {
		log.Logrus.Debugln(err)
		return err
	} else { //grpc调用成功， imResp中带着处理结果
		log.Logrus.Debugln("grpc success and return:", imResp.Out)
		return nil
	}
}

//向其他chan转发新消息通知，
//新消息通知消息中需要提供toid及deviceType。其中toid指定转发到那个用户，deviceType指定转发到那个端
//需要显式的声明返回值err，要不函数内部崩了，recover返回是nil。 显式声明能带回来err
func ForwardMsgToCh(msg *pb.MsgSt) (err error) {
	defer func() {
		//从Map中获取writerCh到向writerCh中加入消息之间很可能因ch被关闭掉而崩溃，尤其是在用户批量上线和下线的时候
		if err := recover(); err != nil {
			log.Logrus.Errorln("forwardMsgToCh recover : ", err)
		}
	}()

	var useridmap string
	useridmap, err = reviseUserid(msg.ToId, msg.Device)
	if err != nil {
		log.Logrus.Errorln("ForwardMsgToCh: ", msg.String(), msg.ServerMsgId, err)
		return err
	}

	var ch chan<- pb.MsgSt
	ch, err = GetChFromMap(useridmap)
	if err != nil {
		log.Logrus.Errorln("ForwardMsgToCh: ", msg.String(), msg.ServerMsgId, err)
		return err
	}

	select {
	case ch <- *msg:
		return nil
	case <-time.After(ForwardMsgTimeout * time.Millisecond):
		log.Logrus.Errorln(ch, useridmap, "ForwardMsgToCh Msg timeout!", msg.String(), msg.ServerMsgId)
		return errors.New("local forward Msg timeout!")
	}
}

//转发私聊消息
//记录完redis后，向消息的发送者回ack, 然后开启转发逻辑
//接收者和发送者同在一个millipede，则直接转发消息. 现在是直接转发新消息通知， 这样多读一次redis， 后续需要改协议，直接转发消息.
//不在同一millipede则grpc转发新消息通知，失败走mq通道
//from_useridmap 用来向消息的发送者回ack
func dispatchMsg(conn *websocket.Conn, msg *pb.MsgSt, from_useridmap string, msgids *[MaxCltMsgId]MsgidSt) {
	fromid := msg.FromId
	toid := msg.ToId
	deviceType := msg.Device
	var toDeviceType pb.DeviceTypeEnum //转发需要的设备类型

	//校验设备类型， 并获取转发需要的设备类型
	if deviceType == pb.DeviceTypeEnum_ANDROID || deviceType == pb.DeviceTypeEnum_IOS {
		toDeviceType = pb.DeviceTypeEnum_WIN
	} else if deviceType == pb.DeviceTypeEnum_WIN || deviceType == pb.DeviceTypeEnum_MAC {
		toDeviceType = pb.DeviceTypeEnum_IOS
	} else {
		log.Logrus.Errorln("dispatchMsg: deviceType invalid", msg.FromId, msg.Device)
		return
	}

	//记录redis成功后回ack
	if AddMsgToRedis(fromid, toDeviceType, msg) && AddMsgToRedisAll(toid, msg) {
		log.Logrus.Debugln(toid, msg.ServerMsgId, "add msg to redis success!")
		Ser2CltMsgAck(conn, from_useridmap, *msg, pb.AckCodeEnum_OK, msg.ServerMsgId, "")
	} else {
		//如果写redis失败，需要删掉排重消息ID
		delMsgid(msg.ClientMsgId, msgids)
		log.Logrus.Debugln(toid, msg.ServerMsgId, "add msg to redis failure!")
		Ser2CltMsgAck(conn, from_useridmap, *msg, pb.AckCodeEnum_INTERNALSERVERERR, msg.ServerMsgId, "")
		return
	}

	//新消息通知，转发给发送者的另一端
	msgNew := &pb.MsgSt{
		ToId:    fromid,
		Device:  toDeviceType,
		MsgType: pb.MsgTypeEnum_NEWMSG,
	}

	if err := ForwardMsgToCh(msgNew); err == nil {
		log.Logrus.Debugln(fromid, "dispatchMsg: local forward msg to src other end!", pb.MsgTypeEnum_NEWMSG)
	}

	//组新消息通知
	msgNew.ToId = toid

	//同一个millipede上, 直接转发
	if IsUserINMap(toid) {
		msgNew.Device = pb.DeviceTypeEnum_WIN
		if err := ForwardMsgToCh(msgNew); err == nil {
			log.Logrus.Debugln(toid, "dispatchMsg: local forward msg to des pc success!")
		}

		msgNew.Device = pb.DeviceTypeEnum_IOS
		if err := ForwardMsgToCh(msgNew); err == nil {
			log.Logrus.Debugln(toid, "dispatchMsg: local forward msg to des mobile success!")
		}

		return
	}

	//不在同一个millipede, 获取toid所在的millipede， 然后grpc转发， 失败走mq
	if millipedeId, err := GetMillipedeByUser(toid); err != nil {
		log.Logrus.Error("dispatchMsg: get millipede failure:", err)
	} else {
		log.Logrus.Debugln("dispatchMsg: to user exist millipede num: ", millipedeId)

		msgNew.Device = pb.DeviceTypeEnum_ALL
		if err := ForwardMsgRpc(millipedeId, msgNew); err != nil {
			PutMsgToMq(millipedeId, msgNew)
		}
	}
}

//如果程序之前没有从timer.C中读取过值，这时需要首先调用Stop()，
//如果返回true，说明timer还没有expire，stop可以成功的删除timer，然后直接reset；
//如果返回false，说明stop前已经expire，需要显式drain channel, 然后reset
func timerStop(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
func timerReset(timer *time.Timer, seconds int) {
	timerStop(timer)
	timer.Reset(time.Duration(seconds) * time.Second)
}

//鉴权服务
func checkToken_grpc(token string) error {
	/*
		//鉴权服务
		agent, err := discover.Discover(bootstrap.GatewayConfig.ConsulAuthName)
		if err != nil {
			return err
		}

		var port string
		if v, ok := agent.Meta["rpcport"]; ok {
			port = v
		} else {
			return errCode.ErrNoGrpcPort
		}

		addr := agent.Address + ":" + port

		conn, err := grpc.Dial(addr, grpc.WithInsecure(), grpc.WithTimeout(1*time.Second))
		if err != nil {
			return err
		}
		defer conn.Close()

		authCli, _ := auth_r.New(conn)
		authResp, err := authCli.Auth(context.TODO(), &auth_pb.AuthRequest{"Check_Token", "", "", "", "", token, ""})

		//鉴权未通过
		if err != nil || authResp.Valid != "true" {
			return errCode.ErrInvalidAToken
		}
	*/
	return nil
}

//发送踢人消息
//参数二是被踢者在redis中存储的信息
//参数三是新需要登录的用户信息
func Kickout(userid string, userInRedis *pb.UserRedisSt, user *UserSt) {

	//组踢人消息
	kickoutMsgTmp := &pb.KickoutMsgSt{
		KickType:     pb.KickTypeEnum_SAMEITEMLOGIN,
		DeviceModel:  user.UserRedis.DeviceModel,
		DeviceId:     userInRedis.DeviceId,
		FromDeviceId: user.UserRedis.DeviceId,
		MillipedeId:  bootstrap.ConsulConfig.InstanceId}

	msg := &pb.MsgSt{
		//FromId:     msg.FromId,
		ToId:       userid,                    //用于转发
		Device:     user.UserRedis.DeviceType, //用于转发
		MsgType:    pb.MsgTypeEnum_KICKOUT,
		KickoutMsg: kickoutMsgTmp}

	if bootstrap.ConsulConfig.InstanceId == userInRedis.MillipedeId {
		//在同一个millipede
		ForwardMsgToCh(msg)
	} else {
		//不在同一个millipede
		ForwardMsgRpc(userInRedis.MillipedeId, msg)
	}
}
