package millipede

import (
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"net/http"
	"net/url"
	"strconv"
	pb "websocket-im/pb"
	"websocket-im/util/bootstrap"
	"websocket-im/util/log"
)

//http://192.168.73.3:5370/manager/map/count
//http://192.168.73.3:5370/manager/map/info?userid=xu_m
func MapManage(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	if v, ok := vars["key"]; ok {
		switch v {
		case "count":
			//查询在线用户数
			Maplock.Lock()
			result := strconv.Itoa(len(UserStMap))
			Maplock.Unlock()
			w.Write([]byte(result))

		case "list":
			//查询在线用户列表
			result := ""
			Maplock.Lock()
			for key := range UserStMap {
				result += key + ";"
			}
			Maplock.Unlock()
			w.Write([]byte(result))

		case "info":
			//解userid
			var useridmap string
			params, _ := url.ParseQuery(r.URL.RawQuery)
			if v, ok := params["userid"]; ok {
				useridmap = v[0]
			} else {
				http.Error(w, http.StatusText(400), http.StatusBadRequest)
			}

			//从Map获取用户信息
			if user, err := GetUserFromMap(useridmap); err != nil {
				w.Write([]byte("get user from map failure"))
			} else {
				w.Write([]byte(user.UserRedis.String()))
			}

		case "delete":
			//解userid
			var useridmap string
			params, _ := url.ParseQuery(r.URL.RawQuery)
			if v, ok := params["userid"]; ok {
				useridmap = v[0]
			} else {
				http.Error(w, http.StatusText(400), http.StatusBadRequest)
			}

			//从map中删除用户
			if err := DelUseFromMap(useridmap); err != nil {
				w.Write([]byte("delete user from map failure"))
			} else {
				w.Write([]byte("delete user from map ok"))
			}
		default:
			http.Error(w, http.StatusText(400), http.StatusBadRequest)
		}
	} else {
		http.Error(w, http.StatusText(400), http.StatusBadRequest)
	}
}

//http://192.168.73.3:5370/manager/redis/info?userid=xu_pc
func RedisManage(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	if v, ok := vars["key"]; ok {
		switch v {
		case "list":
			//查询所有注册用户列表
			if users, err := GetUserMemFromRedis(); err == nil {
				result := ""
				for _, v := range users {
					result += v + ";"
				}
				w.Write([]byte(result))
			} else {
				w.Write([]byte("GetUserMemFromRedis failure"))
			}

		case "group":
			//查询群成员列表 delay
			w.Write([]byte("group ok"))

		case "info":
			//解userid
			var useridmap string
			params, _ := url.ParseQuery(r.URL.RawQuery)
			if v, ok := params["userid"]; ok {
				useridmap = v[0]
			} else {
				http.Error(w, http.StatusText(400), http.StatusBadRequest)
			}

			//从redis获取用户信息
			if user, err := GetUserFromRedis(useridmap); err != nil {
				w.Write([]byte("GetUserFromRedis failure"))
			} else {
				w.Write([]byte(user.String()))
			}

		default:
			http.Error(w, http.StatusText(400), http.StatusBadRequest)
		}
	} else {
		http.Error(w, http.StatusText(400), http.StatusBadRequest)
	}
}

//http://192.168.73.3:5370/manager/msg/offlinemsg?userid=xu_pc
//http://192.168.73.3:5370/manager/msg/dupmsg?userid=xu_pc
func MsgManage(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	if v, ok := vars["key"]; ok {
		switch v {
		case "offlinemsg":
			//解userid
			var useridmap string
			params, _ := url.ParseQuery(r.URL.RawQuery)
			if v, ok := params["userid"]; ok {
				useridmap = v[0]
			} else {
				http.Error(w, http.StatusText(400), http.StatusBadRequest)
			}

			//从redis获取用户离线消息
			var result string
			if userMsg, ok := GetMsgFromRedis(useridmap); ok {
				for _, v := range userMsg.GetMsgContent() {
					result += v.String() + "\n"
				}
				w.Write([]byte(result))
			} else {
				w.Write([]byte(useridmap + " GetMsgFromRedisMq error or mq is null !"))
			}
		case "dupmsg":
			//解userid
			var useridmap string
			params, _ := url.ParseQuery(r.URL.RawQuery)
			if v, ok := params["userid"]; ok {
				useridmap = v[0]
			} else {
				http.Error(w, http.StatusText(400), http.StatusBadRequest)
			}

			//从redis获取用户排重消息id
			var msgids [MaxCltMsgId]MsgidSt
			GetMsgIdFromRedis(useridmap, &msgids)
			var result string
			for j := 0; j < MaxCltMsgId; j++ {
				if msgids[j].CliMsgid == 0 {
					continue
				}
				result += "{" + strconv.Itoa(int(msgids[j].CliMsgid)) + " " + strconv.Itoa(int(msgids[j].SvrMsgid)) + "}" + ";"
			}
			w.Write([]byte(result))
		default:
			http.Error(w, http.StatusText(400), http.StatusBadRequest)
		}
	} else {
		http.Error(w, http.StatusText(400), http.StatusBadRequest)
	}
}

//http://192.168.73.3:5370/manager/cfg/printLevel?level=5
func CfgManage(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	if v, ok := vars["key"]; ok {
		switch v {
		case "printLevel":
			//解level
			var level string
			params, _ := url.ParseQuery(r.URL.RawQuery)
			if v, ok := params["level"]; ok {
				level = v[0]
			} else {
				http.Error(w, http.StatusText(400), http.StatusBadRequest)
			}

			//设置打印级别
			if level == "0" {
				log.Logrus.SetLevel(logrus.PanicLevel)
			} else if level == "1" {
				log.Logrus.SetLevel(logrus.FatalLevel)
			} else if level == "2" {
				log.Logrus.SetLevel(logrus.ErrorLevel)
			} else if level == "3" {
				log.Logrus.SetLevel(logrus.WarnLevel)
			} else if level == "4" {
				log.Logrus.SetLevel(logrus.InfoLevel)
			} else if level == "5" {
				log.Logrus.SetLevel(logrus.DebugLevel)
			} else if level == "6" {
				log.Logrus.SetLevel(logrus.TraceLevel)
			} else {
				http.Error(w, http.StatusText(400), http.StatusBadRequest)
			}

			w.Write([]byte("set log level ok"))

		case "storeMsgDay":
			//解userid
			var day string
			params, _ := url.ParseQuery(r.URL.RawQuery)
			if v, ok := params["level"]; ok {
				day = v[0]
			} else {
				http.Error(w, http.StatusText(400), http.StatusBadRequest)
			}
			//更改离线消息保存天数
			if l, err := strconv.Atoi(day); err != nil {
				http.Error(w, http.StatusText(400), http.StatusBadRequest)
			} else if l < 0 || l > 30 {
				http.Error(w, http.StatusText(400), http.StatusBadRequest)
			} else {
				log.Logrus.Infoln("change MaxDaysOfflineMsg from ", MaxDaysOfflineMsg, " to ", day)
				MaxDaysOfflineMsg = int64(l)
				w.Write([]byte("set store msg days ok"))
			}

		case "msgRate":
			//更改用户发消息阈值 delay
			w.Write([]byte("set msg rate ok"))

		default:
			http.Error(w, http.StatusText(400), http.StatusBadRequest)
		}
	} else {
		http.Error(w, http.StatusText(400), http.StatusBadRequest)
	}
}

//http://192.168.73.3:5370/manager/user/kickout?userid=xu
func UserManage(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	if v, ok := vars["key"]; ok {
		switch v {
		case "kickout":
			var userid string
			params, _ := url.ParseQuery(r.URL.RawQuery)
			if v, ok := params["userid"]; ok {
				userid = v[0]
			} else {
				http.Error(w, http.StatusText(400), http.StatusBadRequest)
				return
			}

			svrKickout(userid, pb.DeviceTypeEnum_WIN)
			svrKickout(userid, pb.DeviceTypeEnum_IOS)

			w.Write([]byte("kickout user ok"))

		default:
			http.Error(w, http.StatusText(400), http.StatusBadRequest)
		}
	} else {
		http.Error(w, http.StatusText(400), http.StatusBadRequest)
	}
}

func svrKickout(userid string, devicetype pb.DeviceTypeEnum) error {
	useridmap, err := reviseUserid(userid, devicetype)
	if err != nil {
		log.Logrus.Errorln("svr kickout: reviseUserid", userid, devicetype, err)
		return err
	}

	userInRedis, err := GetUserFromRedis(useridmap)
	if err != nil {
		return err
	}

	//组踢人消息
	kickoutMsgTmp := &pb.KickoutMsgSt{
		KickType: pb.KickTypeEnum_SRVKICKOUT,
		//DeviceModel:  user.UserRedis.DeviceModel,
		//DeviceId:     userInRedis.DeviceId,
		//FromDeviceId: user.UserRedis.DeviceId,
		//MillipedeId:  userInRedis.MillipedeId,
	}

	msg := &pb.MsgSt{
		ToId:       userid,     //用于转发
		Device:     devicetype, //用于转发
		MsgType:    pb.MsgTypeEnum_KICKOUT,
		KickoutMsg: kickoutMsgTmp}

	if bootstrap.ConsulConfig.InstanceId == userInRedis.MillipedeId {
		//在同一个millipede
		ForwardMsgToCh(msg)
	} else {
		//不在同一个millipede
		ForwardMsgRpc(userInRedis.MillipedeId, msg)
	}

	return nil
}
