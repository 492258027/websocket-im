package consul

import (
	"fmt"
	consul "github.com/hashicorp/consul/api"
	"log"
	"websocket-im/util/grpclb/common"
)

type consulRegister struct {
	cli        *consul.Client
	instanceId string //unregister使用
}

func NewConsulRegister(addr string) (*consulRegister, error) {
	c, err := consul.NewClient(&consul.Config{
		Address: addr,
	})
	if err != nil {
		return nil, err
	}

	return &consulRegister{
		cli: c,
	}, nil
}

func (r *consulRegister) Register(service *common.ServiceInfo, interval int) error {

	//保存instanceId， deRegister用
	r.instanceId = service.InstanceId

	serviceRegistration := &consul.AgentServiceRegistration{
		ID:      service.InstanceId,
		Name:    service.SerName + ":" + service.Version,
		Address: service.Ip,
		Port:    service.Port,
		Meta:    service.Metadata,
		Check: &consul.AgentServiceCheck{
			DeregisterCriticalServiceAfter: "3s",
			//HTTP:                           "http://" + service.Address + healthCheckUrl,
			// grpc 支持，执行健康检查的地址，service 会传到 Health.Check 函数中
			GRPC:     fmt.Sprintf("%v:%v/%v:%v", service.Ip, service.Port, service.SerName, service.Version),
			Interval: fmt.Sprintf("%ds", interval),
		},
	}

	if err := r.cli.Agent().ServiceRegister(serviceRegistration); err != nil {
		log.Println("Register Service Error!", err)
		return err
	}
	log.Println("Register Service Success!")
	return nil
}

func (r *consulRegister) Unregister() error {
	// 发送服务注销请求
	if err := r.cli.Agent().ServiceDeregister(r.instanceId); err != nil {
		log.Println("Deregister Service Error!")
		return err
	}
	log.Println("Deregister Service Success!")
	return nil
}

//封装一下， 方便用户调用, 用不用均可
func InitRegister(consulhost string, consulport int, service *common.ServiceInfo, interval int) (*consulRegister, error) {
	r, err := NewConsulRegister(fmt.Sprintf("%s:%d", consulhost, consulport))
	if err != nil {
		log.Println("Fail to new client", err)
		return nil, err
	}

	if err := r.Register(service, interval); err != nil {
		log.Println("Fail to register service", err)
		return nil, err
	}

	return r, nil
}
