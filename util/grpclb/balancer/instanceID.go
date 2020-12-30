package balancer

import (
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
	"log"
	"sync"
	"websocket-im/util/grpclb/common"
)

const InstanceID = "instance_x"

func init() {
	balancer.Register(newInstanceBuilder())
}

func newInstanceBuilder() balancer.Builder {
	return base.NewBalancerBuilder(InstanceID, &instancePickerBuilder{}, base.Config{HealthCheck: true})
}

type instancePickerBuilder struct {
}

//每次地址集有改变，均调用
func (r *instancePickerBuilder) Build(buildInfo base.PickerBuildInfo) balancer.Picker {

	if len(buildInfo.ReadySCs) == 0 {
		return base.NewErrPicker(balancer.ErrNoSubConnAvailable)
	}

	picker := &instancePicker{
		subConns: make(map[string]balancer.SubConn),
	}

	for subConn, subConnInfo := range buildInfo.ReadySCs {
		if id, err := common.GetInstanceId(subConnInfo.Address); err == nil {
			picker.subConns[id] = subConn
		}
	}

	log.Println("build subConn: ", picker.subConns)

	return picker
}

type instancePicker struct {
	subConns map[string]balancer.SubConn
	mu       sync.Mutex
}

func (p *instancePicker) Pick(info balancer.PickInfo) (balancer.PickResult, error) {
	ret := balancer.PickResult{}
	p.mu.Lock()
	if v, ok := p.subConns[info.Ctx.Value(common.InstanceId).(string)]; ok {
		ret.SubConn = v
	}

	log.Println("pick SubConn: ", ret.SubConn)
	p.mu.Unlock()
	return ret, nil
}
