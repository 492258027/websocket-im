package main

import (
	"fmt"
	"github.com/gorilla/mux"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	h "websocket-im/handlers"
	m "websocket-im/millipede"
	pb "websocket-im/pb"
	"websocket-im/util/bootstrap"
	"websocket-im/util/grpclb/balancer"
	"websocket-im/util/grpclb/common"
	"websocket-im/util/grpclb/consul"
	"websocket-im/util/log"
	"websocket-im/util/rabbitmq"
	"websocket-im/util/redis"
	"websocket-im/util/snowflake"
)

func GetDockerIp() ([]string, error) {
	addrs, err := net.InterfaceAddrs()

	if err != nil {
		return nil, err
	}

	var host []string
	for _, address := range addrs {
		// 检查ip地址判断是否回环地址
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				host = append(host, ipnet.IP.String())
			}
		}
	}

	return host, nil
}

// Run starts a new http server, gRPC server, and a debug server with the
// passed config and logger
func main() {
	//在docker中运行，配置文件不需要配置host，自动获取docker的地址
	if bootstrap.HttpConfig.Host == "" || bootstrap.RpcConfig.Host == "" {
		dockerips, err := GetDockerIp()
		if err != nil {
			log.Logrus.Fatalln("Fail to get docker address", err)
		}
		bootstrap.HttpConfig.Host = dockerips[0]
		bootstrap.RpcConfig.Host = dockerips[0]
	}

	//注册到consul
	r, err := consul.InitRegister(bootstrap.ConsulConfig.Host, bootstrap.ConsulConfig.Port, &common.ServiceInfo{
		InstanceId: bootstrap.ConsulConfig.InstanceId,
		SerName:    bootstrap.ConsulConfig.ServiceName,
		Version:    bootstrap.ConsulConfig.Version,
		Ip:         bootstrap.RpcConfig.Host,
		Port:       bootstrap.RpcConfig.Port,
		Metadata: map[string]string{
			common.InstanceId: bootstrap.ConsulConfig.InstanceId,
		},
	}, 5)
	if err != nil {
		log.Logrus.Fatalln("Fail to register consul", err)
	}
	defer r.Unregister()

	//初始化millipede之间相互转发信息的 grpc client
	conn, err := consul.InitResolver(bootstrap.ConsulConfig.Host, bootstrap.ConsulConfig.Port,
		bootstrap.ConsulConfig.ServiceName + ":" + bootstrap.ConsulConfig.Version, balancer.InstanceID)
	if err != nil {
		log.Logrus.Fatalln("Fail to resolver consul", err)
	}
	defer conn.Close()
	m.GrpcClient = pb.NewIMClient(conn)

	//初始化鉴权服务的grpc client, delay

	//初始化snowflake
	snowflake.InitSnowFlake(bootstrap.ConsulConfig.InstanceId)

	//初始化redis
	redis.InitRedis(bootstrap.Redisconfig.ClusterIPs, bootstrap.Redisconfig.PoolSize,
		bootstrap.Redisconfig.MinIdleConns, bootstrap.Redisconfig.Password)

	//初始化Mq
	rabbitmq.InitRabbitMq(bootstrap.Mqconfig.PoolSize, bootstrap.Mqconfig.AmqpURI,
		bootstrap.Mqconfig.ExchangeName, bootstrap.Mqconfig.ExchangeType,
		bootstrap.Mqconfig.QueueName, bootstrap.Mqconfig.RoutingKey)

	//初始化map表
	m.InitUserStMap()

	//启动routine，从mq接收消息并分发到对应的writerCh
	go m.GetDataFromMq()

	//启动routine，定期删除redis中的过期消息，delay 修改测试
	//go im.DelMsgRoutine()

	// Mechanical domain.
	errc := make(chan error)
	go InterruptHandler(errc)

	//webscoket server
	go func() {
		log.Logrus.Debugln("IM", "websocket", "addr", bootstrap.Wsconfig.Host, bootstrap.Wsconfig.Port)
		addr := bootstrap.Wsconfig.Host + ":" + strconv.Itoa(bootstrap.Wsconfig.Port)

		r := mux.NewRouter()
		r.HandleFunc("/IM", m.ReadMsg)
		errc <- http.ListenAndServe(addr, r)
	}()

	//manager server
	go func() {
		log.Logrus.Debugln("IM", "manager", "addr", bootstrap.Mgrconfig.Host, bootstrap.Mgrconfig.Port)
		addr := bootstrap.Mgrconfig.Host + ":" + strconv.Itoa(bootstrap.Mgrconfig.Port)

		r := mux.NewRouter()
		r.HandleFunc("/manager/map/{key:[a-z,A-Z,0-9,_]+}", m.MapManage)
		r.HandleFunc("/manager/redis/{key:[a-z,A-Z,0-9,_]+}", m.RedisManage)
		r.HandleFunc("/manager/msg/{key:[a-z,A-Z,0-9,_]+}", m.MsgManage)
		r.HandleFunc("/manager/cfg/{key:[a-z,A-Z,0-9,_]+}", m.CfgManage)
		r.HandleFunc("/manager/user/{key:[a-z,A-Z,0-9,_]+}", m.UserManage)
		errc <- http.ListenAndServe(addr, r)
	}()

	// gRPC server.
	go func() {
		log.Logrus.Debugln("IM", "gRPC", "addr", bootstrap.RpcConfig.Host, bootstrap.RpcConfig.Port)
		addr := bootstrap.RpcConfig.Host + ":" + strconv.Itoa(bootstrap.RpcConfig.Port)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			errc <- err
			return
		}

		srv := h.ImService{}
		s := grpc.NewServer()
		pb.RegisterIMServer(s, srv)
		//consul的健康检查
		healthsrv := h.HealthImpl{}
		grpc_health_v1.RegisterHealthServer(s, &healthsrv)

		errc <- s.Serve(ln)
	}()

	// Run!
	log.Logrus.Debugln("exit", <-errc)
}

func InterruptHandler(errc chan<- error) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	terminateError := fmt.Errorf("%s", <-c)

	// Place whatever shutdown handling you want here

	errc <- terminateError
}