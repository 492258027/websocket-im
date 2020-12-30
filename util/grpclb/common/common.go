package common

import (
	"errors"
	"google.golang.org/grpc/resolver"
	"strconv"
)

const (
	WeightKey  = "weight"
	InstanceId = "instanceId"
)

type ServiceInfo struct {
	InstanceId string
	SerName    string
	Version    string
	Ip         string
	Port       int
	Metadata   map[string]string
}

func GetWeight(addr resolver.Address) int {

	w := addr.Attributes.Value(WeightKey)
	if w != nil {
		if m, ok := w.(string); ok {
			if weight, err := strconv.Atoi(m); err == nil {
				return weight
			}
		}
	}

	//解失败返回1
	return 1
}

func GetInstanceId(addr resolver.Address) (string, error) {
	id := addr.Attributes.Value(InstanceId)
	if id != nil {
		if m, ok := id.(string); ok {
			return m, nil
		}
	}

	return "", errors.New("GetInstanceId failure!")
}
