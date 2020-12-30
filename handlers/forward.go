package handlers

import (
	"context"
	"errors"
	"google.golang.org/grpc/health/grpc_health_v1"
	m "websocket-im/millipede"
	pb "websocket-im/pb"
	"websocket-im/util/log"
)

type ImService struct{}

//grpc服务的处理函数。 转发的是新消息通知。 查map表并放入对应chan
//grpc返回成功只是代表grpc服务在超时之前执行了一遍，并不代表一定写入到touser对应的chan
func (s ImService) Forward(ctx context.Context, in *pb.MsgSt) (*pb.ForwardResponse, error) {

	//判断微服务传递到这个函数之前是否已经超时
	select {
	case <-ctx.Done():
		println("grpc timeout")
		return &pb.ForwardResponse{Out: "failure"}, errors.New("grpc timeout！")
	default:
	}

	switch in.Device {
	case pb.DeviceTypeEnum_WIN, pb.DeviceTypeEnum_IOS:
		if err := m.ForwardMsgToCh(in); err == nil {
			log.Logrus.Debugln(in.ToId, in.Device, "local forward msg success!")
		}
	case pb.DeviceTypeEnum_ALL:
		in.Device = pb.DeviceTypeEnum_WIN
		if err := m.ForwardMsgToCh(in); err == nil {
			log.Logrus.Debugln(in.ToId, in.Device, "local forward msg to pc success!")
		}
		in.Device = pb.DeviceTypeEnum_IOS
		if err := m.ForwardMsgToCh(in); err == nil {
			log.Logrus.Debugln(in.ToId, "local forward msg to phone success!")
		}
	}

	return &pb.ForwardResponse{Out: "ok"}, nil
}

///////////////////////////////////////// HealthImpl 健康检查实现/////////////////////////////////////////////

type HealthImpl struct{}

// Check 实现健康检查接口，这里直接返回健康状态，这里也可以有更复杂的健康检查策略，比如根据服务器负载来返回
func (h *HealthImpl) Check(ctx context.Context, req *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	//log.Println("health checking\n")
	return &grpc_health_v1.HealthCheckResponse{
		Status: grpc_health_v1.HealthCheckResponse_SERVING,
	}, nil
}

func (h *HealthImpl) Watch(req *grpc_health_v1.HealthCheckRequest, w grpc_health_v1.Health_WatchServer) error {
	return nil
}
