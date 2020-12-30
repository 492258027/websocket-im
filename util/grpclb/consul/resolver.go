package consul

import (
	"fmt"
	"github.com/hashicorp/consul/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/attributes"
	"google.golang.org/grpc/resolver"
	"websocket-im/util/grpclb/common"
	"log"
)

const schema = "juzhouyun"

type consulResolver struct {
	cli       *api.Client
	cc        resolver.ClientConn
	lastIndex uint64
}

func NewConsulResolver(addr string) resolver.Builder {
	cli, err := api.NewClient(&api.Config{
		Address: addr,
	})
	if err != nil {
		log.Fatal("new client:", err)
	}

	return &consulResolver{
		cli:       cli,
		lastIndex: 0,
	}
}

//只在dail时调用一次
func (r *consulResolver) Build(target resolver.Target, cc resolver.ClientConn, opts resolver.BuildOptions) (resolver.Resolver, error) {
	r.cc = cc
	go r.watcher(target.Endpoint)
	return r, nil
}

//每次改变时触发
func (r *consulResolver) watcher(serName string) {
	for {
		entries, metainfo, err := r.cli.Health().Service(serName, "", true, &api.QueryOptions{WaitIndex: r.lastIndex})
		if err != nil {
			log.Println("health error! ", serName)
		}

		r.lastIndex = metainfo.LastIndex
		log.Println("watcher lastIndex", r.lastIndex)

		var addrs []resolver.Address

		for _, ev := range entries {

			agent := ev.Service

			//var att *attributes.Attributes
			att := attributes.New()
			//组weight
			if v, ok := agent.Meta[common.WeightKey]; ok {
				att = att.WithValues(common.WeightKey, v)
			}
			//组instanceId
			if v, ok := agent.Meta[common.InstanceId]; ok {
				att = att.WithValues(common.InstanceId, v)
			}

			//组resolver.Address
			addr := resolver.Address{
				Addr:       fmt.Sprintf("%s:%d", agent.Address, agent.Port),
				Attributes: att,
			}

			addrs = append(addrs, addr)
		}

		r.cc.UpdateState(resolver.State{Addresses: addrs})
	}
}

func (r *consulResolver) Scheme() string {
	return schema
}

func (r *consulResolver) ResolveNow(rn resolver.ResolveNowOptions) {
	log.Println("ResolveNow")
}

func (r *consulResolver) Close() {
	log.Println("Close")
}

//封装一下， 方便用户调用, 用不用均可
func InitResolver(consulhost string, consulport int, serName, balbancerName string) (*grpc.ClientConn, error) {

	r := NewConsulResolver(fmt.Sprintf("%s:%d", consulhost, consulport))
	resolver.Register(r)

	conn, err := grpc.Dial(
		fmt.Sprintf("%s:///%s", r.Scheme(), serName),
		grpc.WithBalancerName(balbancerName),
		grpc.WithInsecure(),
	)
	if err != nil {
		log.Println("net.Connect err", err)
		return nil, err
	}

	return conn, nil
}
