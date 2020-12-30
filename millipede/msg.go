package millipede

import (
	"github.com/golang/protobuf/proto"
	"github.com/gorilla/websocket"
	"strconv"
	"time"
	pb "websocket-im/pb"
	"websocket-im/util/log"
	"websocket-im/util/redis"
	"websocket-im/util/snowflake"
)

//回Ack
func Ser2CltAck(conn *websocket.Conn, useridmap string, msg pb.MsgSt, ackCode pb.AckCodeEnum, des string) error {
	return Ser2CltMsgAck(conn, useridmap, msg, ackCode, snowflake.GetSnowflakeId(), des)
}

//回Ack，自带server端消息id
func Ser2CltMsgAck(conn *websocket.Conn, useridmap string, msg pb.MsgSt, ackCode pb.AckCodeEnum, svrmsgid int64, des string) error {
	ackMsgTmp := &pb.Server2ClientAckMsgSt{
		ClientMsgId: msg.ClientMsgId,
		Code:        ackCode,
		Des:         des,
		MsgType:     msg.MsgType}

	sendMsg := &pb.MsgSt{
		ServerMsgId:   svrmsgid,
		MsgType:       pb.MsgTypeEnum_ACK,
		Ser2CliAckMsg: ackMsgTmp}

	sendMsgs := &pb.ServerSendMsgs{MsgContent: []*pb.MsgSt{sendMsg}}

	data, err := proto.Marshal(sendMsgs)
	//data, err := sendMsgs.Marshal()
	if err != nil {
		log.Logrus.Errorln(useridmap, "Ser2CltMsgAck marshaling error: ", conn.RemoteAddr().String(), err, sendMsg)
		return err
	} else {
		if err = conn.WriteMessage(websocket.TextMessage, data); err != nil {
			log.Logrus.Errorln("Ser2CltMsgAck  WriteMessage error: ", conn.RemoteAddr().String(), err, sendMsg)
			return err
		}
		log.Logrus.Debugln("<---", useridmap, sendMsg.String())
		return nil
	}
}

//回心跳包
func Ser2CltHeartAck(conn *websocket.Conn, useridmap string) error {
	sendMsg := &pb.MsgSt{MsgType: pb.MsgTypeEnum_HEARTBEAT}
	sendMsgs := &pb.ServerSendMsgs{MsgContent: []*pb.MsgSt{sendMsg}}

	data, err := proto.Marshal(sendMsgs)
	//data, err := sendMsgs.Marshal()
	if err != nil {
		log.Logrus.Errorln("Ser2CltHeartAck marshaling error: ", conn.RemoteAddr().String(), err, sendMsg)
		return err
	} else {
		if err = conn.WriteMessage(websocket.TextMessage, data); err != nil {
			log.Logrus.Errorln("Ser2CltHeartAck  WriteMessage error: ", conn.RemoteAddr().String(), err, sendMsg)
			return err
		}
		log.Logrus.Debugln("<---", useridmap, sendMsgs.String())
		return nil
	}
}

//////////////////////////////////////以下为接收者消息队列/////////////////////////////////////////////////
//从接收者队列读取消息，批量操作
func GetMsgFromRedis(useridmap string) (*pb.ServerSendMsgs, bool) {
	var data []interface{}
	tmp := redis.RedisClient.ZRange("ss_"+useridmap+"_mq", 0, int64(MsgBatchNum)).Val()
	for _, v := range tmp {
		data = append(data, []byte(v))
	}

	LEN := len(data)
	if LEN == 0 {
		log.Logrus.Debugln(useridmap, "redis消息队列空")
		return nil, false
	}

	if LEN > MsgBatchNum+1 {
		log.Logrus.Errorln(useridmap, "redis获取消息结果错误", LEN)
		return nil, false
	}

	msgs := &pb.ServerSendMsgs{}
	for _, v := range data {
		//msg必须在for循环内声明,否则所有msgs.MsgContent[i]内容都指向最后一个msg
		msg := &pb.MsgSt{}

		err := proto.Unmarshal(v.([]byte), msg)
		//err := msg.Unmarshal(v.([]byte))
		if err != nil {
			log.Logrus.Errorln(useridmap, "getMsgFromRedis unmarshaling error: ", err)
			return nil, false
		}
		msgs.MsgContent = append(msgs.MsgContent, msg)
	}

	log.Logrus.Debugln(useridmap, "GetMsgFromRedis msgs :", msgs)

	return msgs, true
}

//接收者队列删除消息, 批量操作
func DelMsgFromRedis(useridmap string, acks *pb.Client2ServerAckMsgSt) bool {
	pipe := redis.RedisClient.Pipeline()
	for _, serMsgId := range acks.GetServerMsgId() {
		score := strconv.FormatInt(serMsgId, 10)
		pipe.ZRemRangeByScore("ss_"+useridmap+"_mq", score, score)
	}
	_, err := pipe.Exec()
	if err != nil {
		log.Logrus.Errorln(useridmap, "DelMsgFromRedisMq exec失败:", err.Error())
		return false
	}

	return true
}

//向接收者队列写入消息
func AddMsgToRedis(userid string, deviceType pb.DeviceTypeEnum, msg *pb.MsgSt) bool {
	useridmap, err := reviseUserid(userid, deviceType)
	if err != nil {
		log.Logrus.Errorln("AddMsgToRedis: ", err)
		return false
	}

	data, err := proto.Marshal(msg)
	//data, err := msg.Marshal()
	if err != nil {
		log.Logrus.Errorln(useridmap, "AddMsgToRedis marshaling error: ", err.Error())
		return false
	}

	_, err = redis.RedisClient.ZAdd("ss_"+useridmap+"_mq", redis.Member(msg.ServerMsgId, data)).Result()
	if err != nil {
		log.Logrus.Errorln(useridmap, "向redis消息队列加消息失败:", err.Error())
		return false
	}

	log.Logrus.Debugln(useridmap, "AddMsgToRedis", msg)

	return true
}

//向接收者队列写入消息，多端
func AddMsgToRedisAll(userid string, msg *pb.MsgSt) bool {
	data, err := proto.Marshal(msg)
	//data, err := msg.Marshal()
	if err != nil {
		log.Logrus.Errorln(userid, "AddMsgToRedisMq marshaling error: ", err.Error())
		return false
	}

	pipe := redis.RedisClient.Pipeline()

	pipe.ZAdd("ss_"+userid+"_m"+"_mq", redis.Member(msg.ServerMsgId, data)).Result()
	pipe.ZAdd("ss_"+userid+"_pc"+"_mq", redis.Member(msg.ServerMsgId, data)).Result()

	_, err = pipe.Exec()
	if err != nil {
		log.Logrus.Errorln("AddMsgToRedisAll exec失败:", err.Error())
		return false
	}

	return true
}

//////////////////////////////////////以下消息排重相关/////////////////////////////////////////////////
type MsgidSt struct {
	CliMsgid int64
	SvrMsgid int64
}

//根据客户端生成的消息id来排重，如果是重复消息返回以前的svrmsgidold和index，如果是新消息添加到数组中。 后续可以改为切片
// climsgid 端上的消息id
// svrmsgid 对应climsgid，服务端新生成的消息id
// msgids 数组的指针，数组类型是MsgidSt
// svrmsgidold 对应climsgid，服务端以前生成的消息id。有可能是客户端没收到服务端的ack，重发的消息
// index 在数组中的位置
func judgeDuplication(climsgid, svrmsgid int64, msgids *[MaxCltMsgId]MsgidSt, svrmsgidold *int64, index *int) bool {
	//从数组[MaxCltMsgId-1]开始比较
	for i := MaxCltMsgId - 1; i >= 0; i-- {
		if climsgid == msgids[i].CliMsgid {
			*svrmsgidold = msgids[i].SvrMsgid
			return true
		}
	}

	msgids[*index].CliMsgid = climsgid
	msgids[*index].SvrMsgid = svrmsgid
	*index = (*index + 1) % MaxCltMsgId

	return false
}

//从消息id集合中删掉处理失败的msgid,如客户端重发还可以继续处理
func delMsgid(delmsgid int64, msgids *[MaxCltMsgId]MsgidSt) {
	//从数组[MaxCltMsgId-1]开始比较
	for i := MaxCltMsgId - 1; i >= 0; i-- {
		if delmsgid == msgids[i].CliMsgid {
			msgids[i].CliMsgid = 0
			msgids[i].SvrMsgid = 0
			return
		}
	}
}

//从redis hash表获取client消息ID
func GetMsgIdFromRedis(useridmap string, msgids *[MaxCltMsgId]MsgidSt) {
	var num int
	var err error

	defer func(i int) {
		if err := recover(); err != nil {
			log.Logrus.Errorln(useridmap, "GetMsgIdFromRedis error : ", err, i)
		}
	}(num)

	tmp, err := redis.RedisClient.HGetAll("hash_" + useridmap + "_msgids").Result()
	num = len(tmp)
	if err == nil && num > 0 && num <= MaxCltMsgId {
		i := MaxCltMsgId - 1
		for k, v := range tmp {
			key, err := strconv.ParseInt(k, 10, 64)
			if err != nil {
				log.Logrus.Errorln(useridmap, "ParseInt 取历史client消息id:", err.Error())
				return
			}
			value, err1 := strconv.ParseInt(v, 10, 64)
			if err1 != nil {
				log.Logrus.Errorln(useridmap, "ParseInt 取历史client消息id:", err1.Error())
				return
			}
			msgids[i].CliMsgid = key
			msgids[i].SvrMsgid = value
			i--
		}
	} else {
		log.Logrus.Errorln(useridmap, "SCard 取历史client消息id error!", num)
	}

	log.Logrus.Debugln(useridmap, "历史消息id:", *msgids)
}

//把client消息ID落地到redis hash表
func AddMsgIdToRedis(useridmap string, msgids *[MaxCltMsgId]MsgidSt) {
	if useridmap == "" {
		log.Logrus.Errorln("AddMsgIdToRedis : param null")
		return
	}

	redis.RedisClient.Del("hash_" + useridmap + "_msgids")

	pipe := redis.RedisClient.Pipeline()
	//清空原有记录
	//pipe.Del("hash_" + useridmap + "_msgids")
	//写入redis新的记录
	for i := MaxCltMsgId - 1; i >= 0; i-- {
		if msgids[i].CliMsgid == 0 {
			continue
		}
		pipe.HSet("hash_"+useridmap+"_msgids", strconv.FormatInt(msgids[i].CliMsgid, 10), strconv.FormatInt(msgids[i].SvrMsgid, 10))
	}
	_, err := pipe.Exec()
	if err != nil {
		log.Logrus.Errorln("AddMsgIdToRedis exec失败:", err.Error())
	}
}

//////////////////////////////////////以下定期清理过期未读消息/////////////////////////////////////////////////
//定时清理超过历史消息保存时间的未读消息
func DelMsgRoutine() {
	for {
		//凌晨5-6点执行
		if time.Now().Hour() == 5 {
			//新建定时器,hours小时之后才会执行第一次删除
			timerDelMsg := time.NewTimer(time.Hour * 24 * time.Duration(MaxDaysOfflineMsg))
			for {
				select {
				case <-timerDelMsg.C:
					//复位定时器,24小时后执行下一次,即最短保存时间为一天
					timerDelMsg.Reset(time.Hour * 24)

					DelExpireMsg(MaxDaysOfflineMsg * 24)
				}
			}
		}
		time.Sleep(50 * time.Minute)
	}
}

//定时清理超过历史消息保存时间的未读消息
func DelExpireMsg(hours int64) {
	//计算要删除的最大msgid
	timestamp := time.Now().UnixNano()/1e6 - snowflake.RefTime
	timestamp -= hours * 60 * 60 * 1000
	maxMsgid := timestamp << 13

	//获得所有userid
	users, err := GetUserMemFromRedis()
	if err != nil {
		log.Logrus.Errorln("DelExpireMsg GetUserMemFromRedis error:", err.Error())
		return
	}

	score := strconv.FormatInt(maxMsgid, 10)
	//遍历redis里的所有mq,删除过期消息
	pipe := redis.RedisClient.Pipeline()

	log.Logrus.Debugln("begin to DelExpireMsg :", score)
	for _, userid := range users {
		useridmap, _ := reviseUserid(userid, pb.DeviceTypeEnum_ANDROID)
		pipe.ZRemRangeByScore("ss_"+useridmap+"_mq", "0", score)

		useridmap, _ = reviseUserid(userid, pb.DeviceTypeEnum_WIN)
		pipe.ZRemRangeByScore("ss_"+useridmap+"_mq", "0", score)
	}
	_, err = pipe.Exec()
	if err != nil {
		log.Logrus.Errorln("DelExpireMsg exec error:", err.Error())
		return
	}

	log.Logrus.Debugln("complete DelExpireMsg.")
}
