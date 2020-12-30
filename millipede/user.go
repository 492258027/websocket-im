package millipede

import (
	"errors"
	redis_ori "github.com/go-redis/redis"
	"strconv"
	"strings"
	"sync"
	pb "websocket-im/pb"
	"websocket-im/util/log"
	"websocket-im/util/redis"
)

//user状态信息,移动端和PC端共用一个结构和map,map的key为userid_m和userid_pc
type UserSt struct {
	WriterCh  chan pb.MsgSt  //消息channel
	UserRedis pb.UserRedisSt //需要保存在redis里的内容
}

var Maxuser = 10000
var UserStMap map[string]*UserSt
var Maplock sync.RWMutex

func InitUserStMap() {
	UserStMap = make(map[string]*UserSt, Maxuser)
}

///////////////////////////以下为map的基本操作///////////////////////////////////////
func ModifyMap(useridmap string, userStat pb.UserStatEnum) error {
	if useridmap == "" {
		log.Logrus.Errorln("ModifyMap: param null")
		return errors.New("ModifyMap: param null")
	}

	Maplock.Lock()
	defer Maplock.Unlock()

	//修改map表中用户状态
	value, ok := UserStMap[useridmap]
	if ok {
		if userStat != pb.UserStatEnum_OFFLINE {
			value.UserRedis.Userstat = userStat
		} else if value.UserRedis.Userstat != pb.UserStatEnum_ACTIVEQUIT {
			//要区分是client还是handleroutine断开链接，如果用户主动退出登录或被服务器踢下线不会更改为offline
			value.UserRedis.Userstat = userStat
		}
		log.Logrus.Infoln(useridmap, "ModifyMap success!", userStat)
		return nil
	} else {
		log.Logrus.Errorln(useridmap, "ModifyMap: can not find")
		return errors.New("ModifyMap: can not find:" + useridmap)
	}
}

//map表操作
func ModifyMap_Sleep(useridmap string, userStat pb.UserStatEnum, unReadNum int32) error {
	if useridmap == "" {
		log.Logrus.Errorln("ModifyMap_Sleep: param null")
		return errors.New("ModifyMap_Sleep: param null")
	}

	Maplock.Lock()
	defer Maplock.Unlock()

	//修改map表中用户状态
	value, ok := UserStMap[useridmap]
	if ok {
		value.UserRedis.Userstat = userStat
		value.UserRedis.UnReadNum = unReadNum

		return nil
	} else {
		log.Logrus.Errorln(useridmap, "ModifyMap_Sleep: can not find ")
		return errors.New("ModifyMap_Sleep: can not find:" + useridmap)
	}
}

//通过用户id查找channel
func GetChFromMap(useridmap string) (chan<- pb.MsgSt, error) {
	if useridmap == "" {
		log.Logrus.Errorln("GetChFromMap: param null")
		return nil, errors.New("GetChFromMap: param null")
	}

	Maplock.RLock()
	defer Maplock.RUnlock()

	value, ok := UserStMap[useridmap]
	if ok {
		return value.WriterCh, nil
	} else {
		log.Logrus.Debugln(useridmap, "GetChFromMap: can not find ")
		return nil, errors.New("GetChFromMap: can not find:" + useridmap)
	}
}

//通过用户id查找user
func GetUserFromMap(useridmap string) (*UserSt, error) {
	if useridmap == "" {
		log.Logrus.Errorln("GetUserFromMap: param null")
		return nil, errors.New("GetUserFromMap: param null")
	}

	Maplock.RLock()
	defer Maplock.RUnlock()

	value, ok := UserStMap[useridmap]
	if ok {
		return value, nil
	} else {
		log.Logrus.Errorln(useridmap, "GetUserFromMap: can not find ")
		return nil, errors.New("GetUserFromMap: can not find:" + useridmap)
	}
}

//把用户加入到map表
func AddUserToMap(useridmap string, userInfo *UserSt) error {
	if useridmap == "" {
		log.Logrus.Errorln("AddUserToMap : param null")
		return errors.New("AddUserToMap : param null")
	}

	Maplock.Lock()
	defer Maplock.Unlock()

	//判断用户是否满了
	if len(UserStMap) == Maxuser {
		log.Logrus.Errorln("AddUserToMap : UserStMap full")
		return errors.New("AddUserToMap : UserStMap full")
	}

	tmp, ok := UserStMap[useridmap]
	if ok {
		log.Logrus.Infoln(useridmap, "AddUserToMap : user already exist in map ", *tmp, userInfo.UserRedis.String())
	}
	UserStMap[useridmap] = userInfo

	return nil
}

//通过用户id删除map表中的用户
func DelUseFromMap(useridmap string) error {
	if useridmap == "" {
		log.Logrus.Errorln("DelUseFromMap: param null")
		return errors.New("DelUseFromMap: param null")
	}

	Maplock.Lock()
	defer Maplock.Unlock()

	_, ok := UserStMap[useridmap]
	if ok {
		delete(UserStMap, useridmap)
		return nil
	} else {
		log.Logrus.Errorln(useridmap, "DelUseFromMap: delete user in map failure! ")
		return errors.New("DelUseFromMap: delete user in map failure! " + useridmap)
	}
}

///////////////////////////以下为redis的基本操作///////////////////////////////////////
//密码判断
func verifyPwdFromRedis(userid, passwd string) pb.AckCodeEnum {
	if userid == "" {
		log.Logrus.Errorln("verifyPwdFromRedis 用户名为空!")
		return pb.AckCodeEnum_NAMENOTFOUND
	}

	pwd, err := redis.RedisClient.HGet(userid+"_user_info", "pwd").Result()
	if err != nil {
		log.Logrus.Errorln(userid, "从redis中获取用户pwd信息失败:", err.Error())
		return pb.AckCodeEnum_NAMENOTFOUND
	}

	if pwd == passwd {
		return pb.AckCodeEnum_OK
	} else {
		log.Logrus.Infoln(userid, "密码错误!")
		return pb.AckCodeEnum_PASSWORDERR
	}
}

//从redis获取用户信息
func GetUserFromRedis(useridmap string) (*pb.UserRedisSt, error) {
	if useridmap == "" {
		log.Logrus.Errorln("GetUserFromRedis: param null")
		return nil, errors.New("GetUserFromRedis: param null")
	}

	userInfo := &pb.UserRedisSt{}
	var result map[string]string

	result = redis.RedisClient.HGetAll("hash_" + useridmap + "_info").Val()

	tmp, err := strconv.Atoi(result["Userstat"])
	if err != nil {
		log.Logrus.Errorln(useridmap, "GetUserFromRedis: get userUserstat info fail!", err.Error())
		return nil, errors.New("GetUserFromRedis: get user info failure from redis!")
	}
	userInfo.Userstat = pb.UserStatEnum(tmp)
	userInfo.Version = result["Version"]
	tmp, err = strconv.Atoi(result["DeviceType"])
	if err != nil {
		log.Logrus.Errorln(useridmap, "GetUserFromRedis: get userDeviceType info fail!", err.Error())
		return nil, errors.New("GetUserFromRedis: get user info failure from redis!")
	}
	userInfo.DeviceType = pb.DeviceTypeEnum(tmp)
	userInfo.DeviceModel = result["DeviceModel"]
	userInfo.DeviceId = result["DeviceId"]
	userInfo.MillipedeId = result["MillipedeId"]
	tmp, err = strconv.Atoi(result["AndroidType"])
	if err != nil {
		log.Logrus.Errorln(useridmap, "GetUserFromRedis: get userAndroidType info fail!", err.Error())
	} else {
		userInfo.PhoneType = pb.PhoneTypeEnum(tmp)
	}
	userInfo.PushToken = result["PushToken"]
	tmp, err = strconv.Atoi(result["UnReadNum"])
	if err != nil {
		log.Logrus.Errorln(useridmap, "GetUserFromRedis: get userUnReadNum info fail!", err.Error())
		return nil, errors.New("GetUserFromRedis: get user info failure from redis!")
	}
	userInfo.UnReadNum = int32(tmp)
	tmp, err = strconv.Atoi(result["PushSwitch"])
	if err != nil {
		log.Logrus.Errorln(useridmap, "GetUserFromRedis: get userPushSwitch info fail!", err.Error())
		userInfo.PushSwitch = pb.PushSwitchEnum_ON
	} else {
		userInfo.PushSwitch = pb.PushSwitchEnum(tmp)
	}
	tmp, err = strconv.Atoi(result["IsPushDetail"])
	if err != nil {
		log.Logrus.Errorln(useridmap, "GetUserFromRedis: get userIsPushDetail info fail!", err.Error())
		userInfo.IsPushDetail = 1 //1:"昵称:消息",2:"您有一条新消息"
	} else {
		userInfo.IsPushDetail = int32(tmp)
	}

	log.Logrus.Errorln(useridmap, "GetUserFromRedis: ", userInfo.String())

	return userInfo, nil
}

//把用户信息写入到redis
func AddUserToRedis(useridmap string) error {
	if useridmap == "" {
		log.Logrus.Errorln("AddUserToRedis: param null")
		return errors.New("AddUserToRedis: param null")
	}

	user, err := GetUserFromMap(useridmap)
	if err != nil {
		log.Logrus.Errorln(useridmap, err.Error())
		return err
	}

	pipe := redis.RedisClient.Pipeline()
	pipe.HSet("hash_"+useridmap+"_info", "Userstat", strconv.Itoa(int(user.UserRedis.Userstat)))
	pipe.HSet("hash_"+useridmap+"_info", "Version", user.UserRedis.Version)
	pipe.HSet("hash_"+useridmap+"_info", "DeviceType", strconv.Itoa(int(user.UserRedis.DeviceType)))
	pipe.HSet("hash_"+useridmap+"_info", "DeviceModel", user.UserRedis.DeviceModel)
	pipe.HSet("hash_"+useridmap+"_info", "DeviceId", user.UserRedis.DeviceId)
	pipe.HSet("hash_"+useridmap+"_info", "MillipedeId", user.UserRedis.MillipedeId)
	pipe.HSet("hash_"+useridmap+"_info", "UnReadNum", strconv.Itoa(int(user.UserRedis.UnReadNum)))
	_, err = pipe.Exec()

	if err != nil {
		log.Logrus.Errorln(useridmap, "AddUserToRedis: set userinfo to redis fail!", err.Error())
		return errors.New(useridmap + "AddUserToRedis: set user info to redis failure!")
	}

	return nil
}

//
func UserRedisInfo(fromid string, nick *string, useridmap string, unReadnum int32, touserid, groupid string, isDisturb *int) error {
	if fromid == "" || useridmap == "" || nick == nil {
		log.Logrus.Errorln("UserRedisInfo: param null", fromid, useridmap)
		return errors.New("UserRedisInfo: param null")
	}

	uid := ""
	if groupid != "" {
		uids := strings.Split(touserid, "_")
		if len(uids) >= 3 {
			uid = uids[2]
		}
	}

	pipe := redis.RedisClient.Pipeline()
	pipe.HSet("hash_"+useridmap+"_info", "UnReadNum", strconv.Itoa(int(unReadnum)))
	tmp := pipe.HGet(fromid+"_user_info", "name")
	tmp1 := pipe.HGet(groupid+"_"+uid+"_group_conf", "is_disturb")
	tmp2 := pipe.HGet(groupid+"_group_detail", "name")
	pipe.Exec()

	var err error
	*nick = tmp.Val()
	*isDisturb, err = strconv.Atoi(tmp1.Val())
	//没有群设置或者不是群
	if err != nil {
		*isDisturb = 2
	}

	if tmp2.Val() != "" && groupid != "" {
		*nick += "("
		*nick += tmp2.Val()
		*nick += ")"
	}

	return nil
}

//
func GetUsersNick(userids []string) (nick []string, err error) {
	var tmp []*redis_ori.StringCmd
	pipe := redis.RedisClient.Pipeline()
	for _, v := range userids {
		tmp = append(tmp, pipe.HGet(v+"_user_info", "name"))
	}
	_, err = pipe.Exec()
	for _, v := range tmp {
		nick = append(nick, v.Val())
	}

	if err != nil {
		log.Logrus.Errorln("GetUsersNick: set userinfo to redis fail!", err.Error())
		return nil, errors.New("GetUsersNick: set user info to redis failure!")
	}

	return
}

//查询redis中保存的所有用户
func GetUserMemFromRedis() ([]string, error) {
	users, err := redis.RedisClient.SMembers("all_user").Result()
	if err != nil {
		log.Logrus.Errorln("GetUserMemFromRedis: get userinfo fail!", err.Error())
		return nil, errors.New("GetUserMemFromRedis: get user info failure!")
	}

	return users, nil
}

//fromid为发消息的成员, groupid为群id, 返回值为群内除了fromid以外的所有成员
func GetUseridsFromGroup(fromid, groupid string) (bool, []string) {
	var fromidIsInGroup = false

	usersTmp, err := redis.RedisClient.SMembers(groupid + "_group_mem").Result()
	if err != nil {
		log.Logrus.Errorln(fromid, groupid, "从群中获取成员列表失败:", err.Error())
		return false, nil
	}

	userids := []string{}
	for _, user := range usersTmp {
		if user != fromid {
			userids = append(userids, user)
		} else {
			fromidIsInGroup = true
		}
	}

	return fromidIsInGroup, userids
}

///////////////////////////以下为用户相关的基本操作///////////////////////////////////////
//根据userid生成移动端或者pc端的用户id
func reviseUserid(userid string, deviceType pb.DeviceTypeEnum) (string, error) {
	if userid == "" {
		log.Logrus.Errorln("reviseUserid: userid invalid ")
		return "", errors.New("userid invalid")
	}

	if deviceType == pb.DeviceTypeEnum_ANDROID || deviceType == pb.DeviceTypeEnum_IOS {
		return userid + "_m", nil
	} else if deviceType == pb.DeviceTypeEnum_WIN || deviceType == pb.DeviceTypeEnum_MAC {
		return userid + "_pc", nil
	} else {
		log.Logrus.Errorln(userid, "reviseUserid: deviceType invalid", deviceType)
		return "", errors.New("deviceType invalid")
	}
}

//判断用户是否在map中, 不同端有一个在就返回true，优先选此方法，比redis快
func IsUserINMap(userid string) bool {
	useridmap, _ := reviseUserid(userid, pb.DeviceTypeEnum_WIN)
	_, err := GetChFromMap(useridmap)
	if err == nil { //找到了
		return true
	}

	useridmap, _ = reviseUserid(userid, pb.DeviceTypeEnum_IOS)
	_, err = GetChFromMap(useridmap)
	if err == nil { //找到了
		return true
	}

	return false
}

//判断用户在哪个millipede中
func GetMillipedeByUser(userid string) (string, error) {

	useridmap, _ := reviseUserid(userid, pb.DeviceTypeEnum_WIN)
	if userSt, err := GetUserFromRedis(useridmap); err == nil {
		return userSt.MillipedeId, nil
	}

	useridmap, _ = reviseUserid(userid, pb.DeviceTypeEnum_IOS)
	if userSt, err := GetUserFromRedis(useridmap); err == nil {
		return userSt.MillipedeId, nil
	}

	return "", errors.New("invalid millipede")
}

//把登陆的用户信息写入map和redis
func login(useridmap string, user *UserSt) pb.AckCodeEnum {
	//把user结构存入map表
	if AddUserToMap(useridmap, user) != nil {
		return pb.AckCodeEnum_INTERNALSERVERERR
	}

	//修改redis中用户信息
	if AddUserToRedis(useridmap) != nil {
		DelUseFromMap(useridmap)
		return pb.AckCodeEnum_INTERNALSERVERERR
	}

	return pb.AckCodeEnum_OK
}

//多端登录逻辑处理，不同端之间不影响
//redis中无端信息，允许登录
//手动登录， 允许登录
//自动登录， 同一设备允许登录。 不同设备不允许登录. 也就是说允许自动登录的设备id和redis中的设备id需一致。

func LoginCheck(userid string, user *UserSt, auto bool) (logincode pb.AckCodeEnum) {
	defer func() {
		if err := recover(); err != nil {
			log.Logrus.Errorln(userid, "UserLogin error : ", err)
			logincode = pb.AckCodeEnum_INTERNALSERVERERR
		}
	}()

	//判断客户端类型, 并生产新的userid_m 或者 userid_pc
	useridmap, err := reviseUserid(userid, user.UserRedis.DeviceType)
	if err != nil {
		return pb.AckCodeEnum_NAMENOTFOUND
	}

	//从redis中获取用户信息
	userInRedis, err := GetUserFromRedis(useridmap)

	if err != nil {
		//redis里不存在用户信息,允许登录
		logincode = login(useridmap, user)
	} else if auto == false {
		//手动登录，允许登录
		if userInRedis.Userstat == pb.UserStatEnum_ONLINE || userInRedis.Userstat == pb.UserStatEnum_BACKSTAGE {
			//同端在线，先踢人
			Kickout(userid, userInRedis, user)
		}
		logincode = login(useridmap, user)
	} else if userInRedis.DeviceId == user.UserRedis.DeviceId {
		//同一设备，自动登录
		if userInRedis.Userstat == pb.UserStatEnum_ONLINE || userInRedis.Userstat == pb.UserStatEnum_BACKSTAGE {
			//被踢掉的用户在线， 需下发踢人消息
			Kickout(userid, userInRedis, user)
		}
		logincode = login(useridmap, user)
	} else {
		log.Logrus.Errorln(user.WriterCh, useridmap, "User Login forbidden: ")
		return pb.AckCodeEnum_FORBIDDEN
	}

	return logincode
}
