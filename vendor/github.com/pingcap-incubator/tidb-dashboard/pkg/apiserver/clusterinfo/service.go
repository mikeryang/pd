// Copyright 2020 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

// clusterinfo is a directory for ClusterInfoServer, which could load topology from pd
// using Etcd v3 interface and pd interface.

package clusterinfo

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.etcd.io/etcd/clientv3"
	"go.uber.org/fx"

	"github.com/pingcap-incubator/tidb-dashboard/pkg/apiserver/user"
	"github.com/pingcap-incubator/tidb-dashboard/pkg/apiserver/utils"
	"github.com/pingcap-incubator/tidb-dashboard/pkg/httpc"
	"github.com/pingcap-incubator/tidb-dashboard/pkg/pd"
	"github.com/pingcap-incubator/tidb-dashboard/pkg/tidb"
	"github.com/pingcap-incubator/tidb-dashboard/pkg/utils/topology"
)

type ServiceParams struct {
	fx.In
	PDClient   *pd.Client
	EtcdClient *clientv3.Client
	HTTPClient *httpc.Client
	TiDBClient *tidb.Client
}

type Service struct {
	params       ServiceParams
	lifecycleCtx context.Context
}

func NewService(lc fx.Lifecycle, p ServiceParams) *Service {
	s := &Service{params: p}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			s.lifecycleCtx = ctx
			return nil
		},
	})
	return s
}

func Register(r *gin.RouterGroup, auth *user.AuthService, s *Service) {
	endpoint := r.Group("/topology")
	endpoint.Use(auth.MWAuthRequired())
	endpoint.GET("/tidb", s.getTiDBTopology)
	endpoint.DELETE("/tidb/:address", s.deleteTiDBTopology)
	endpoint.GET("/store", s.getStoreTopology)
	endpoint.GET("/pd", s.getPDTopology)
	endpoint.GET("/alertmanager", s.getAlertManagerTopology)
	endpoint.GET("/alertmanager/:address/count", s.getAlertManagerCounts)
	endpoint.GET("/grafana", s.getGrafanaTopology)

	endpoint.GET("/store_location", s.getStoreLocationTopology)

	endpoint = r.Group("/host")
	endpoint.Use(auth.MWAuthRequired())
	endpoint.Use(utils.MWConnectTiDB(s.params.TiDBClient))
	endpoint.GET("/all", s.getHostsInfo)
}

// @Summary Hide a TiDB instance
// @Param address path string true "ip:port"
// @Success 200 "delete ok"
// @Failure 401 {object} utils.APIError "Unauthorized failure"
// @Security JwtAuth
// @Router /topology/tidb/{address} [delete]
func (s *Service) deleteTiDBTopology(c *gin.Context) {
	address := c.Param("address")
	errorChannel := make(chan error, 2)
	ttlKey := fmt.Sprintf("/topology/tidb/%v/ttl", address)
	nonTTLKey := fmt.Sprintf("/topology/tidb/%v/info", address)
	ctx, cancel := context.WithTimeout(s.lifecycleCtx, time.Second*5)
	defer cancel()

	var wg sync.WaitGroup
	for _, key := range []string{ttlKey, nonTTLKey} {
		wg.Add(1)
		go func(toDel string) {
			defer wg.Done()
			if _, err := s.params.EtcdClient.Delete(ctx, toDel); err != nil {
				errorChannel <- err
			}
		}(key)
	}
	wg.Wait()
	var err error
	select {
	case err = <-errorChannel:
	default:
	}
	close(errorChannel)

	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, nil)
}

// @ID getTiDBTopology
// @Summary Get all TiDB instances
// @Success 200 {array} topology.TiDBInfo
// @Router /topology/tidb [get]
// @Security JwtAuth
// @Failure 401 {object} utils.APIError "Unauthorized failure"
func (s *Service) getTiDBTopology(c *gin.Context) {
	instances, err := topology.FetchTiDBTopology(s.lifecycleCtx, s.params.EtcdClient)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, instances)
}

type StoreTopologyResponse struct {
	TiKV    []topology.StoreInfo `json:"tikv"`
	TiFlash []topology.StoreInfo `json:"tiflash"`
}

// @ID getStoreTopology
// @Summary Get all TiKV / TiFlash instances
// @Success 200 {object} StoreTopologyResponse
// @Router /topology/store [get]
// @Security JwtAuth
// @Failure 401 {object} utils.APIError "Unauthorized failure"
func (s *Service) getStoreTopology(c *gin.Context) {
	tikvInstances, tiFlashInstances, err := topology.FetchStoreTopology(s.params.PDClient)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, StoreTopologyResponse{
		TiKV:    tikvInstances,
		TiFlash: tiFlashInstances,
	})
}

// @ID getStoreLocationTopology
// @Summary Get location labels of all TiKV / TiFlash instances
// @Success 200 {object} topology.StoreLocation
// @Router /topology/store_location [get]
// @Security JwtAuth
// @Failure 401 {object} utils.APIError "Unauthorized failure"
func (s *Service) getStoreLocationTopology(c *gin.Context) {
	storeLocation, err := topology.FetchStoreLocation(s.params.PDClient)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, storeLocation)
}

// @ID getPDTopology
// @Summary Get all PD instances
// @Success 200 {array} topology.PDInfo
// @Router /topology/pd [get]
// @Security JwtAuth
// @Failure 401 {object} utils.APIError "Unauthorized failure"
func (s *Service) getPDTopology(c *gin.Context) {
	instances, err := topology.FetchPDTopology(s.params.PDClient)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, instances)
}

// @ID getAlertManagerTopology
// @Summary Get AlertManager instance
// @Success 200 {object} topology.AlertManagerInfo
// @Router /topology/alertmanager [get]
// @Security JwtAuth
// @Failure 401 {object} utils.APIError "Unauthorized failure"
func (s *Service) getAlertManagerTopology(c *gin.Context) {
	instance, err := topology.FetchAlertManagerTopology(s.lifecycleCtx, s.params.EtcdClient)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, instance)
}

// @ID getGrafanaTopology
// @Summary Get Grafana instance
// @Success 200 {object} topology.GrafanaInfo
// @Router /topology/grafana [get]
// @Security JwtAuth
// @Failure 401 {object} utils.APIError "Unauthorized failure"
func (s *Service) getGrafanaTopology(c *gin.Context) {
	instance, err := topology.FetchGrafanaTopology(s.lifecycleCtx, s.params.EtcdClient)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, instance)
}

// @ID getAlertManagerCounts
// @Summary Get current alert count from AlertManager
// @Success 200 {object} int
// @Param address path string true "ip:port"
// @Router /topology/alertmanager/{address}/count [get]
// @Security JwtAuth
// @Failure 401 {object} utils.APIError "Unauthorized failure"
func (s *Service) getAlertManagerCounts(c *gin.Context) {
	address := c.Param("address")
	cnt, err := fetchAlertManagerCounts(s.lifecycleCtx, address, s.params.HTTPClient)
	if err != nil {
		_ = c.Error(err)
		return
	}
	c.JSON(http.StatusOK, cnt)
}

// @ID getHostsInfo
// @Summary Get information of all hosts
// @Description Get information about host in the cluster
// @Success 200 {array} HostInfo
// @Router /host/all [get]
// @Security JwtAuth
// @Failure 401 {object} utils.APIError "Unauthorized failure"
func (s *Service) getHostsInfo(c *gin.Context) {
	db := utils.GetTiDBConnection(c)

	allHostsMap, err := s.fetchAllInstanceHostsMap()
	if err != nil {
		_ = c.Error(err)
		return
	}
	hostsInfo, err := GetAllHostInfo(db)
	if err != nil {
		_ = c.Error(err)
		return
	}

	hostsInfoMap := make(map[string]HostInfo)
	for _, hi := range hostsInfo {
		hostsInfoMap[hi.IP] = hi
	}

	hiList := make([]HostInfo, 0, len(hostsInfo))
	for hostIP := range allHostsMap {
		if hi, ok := hostsInfoMap[hostIP]; ok {
			hiList = append(hiList, hi)
		} else {
			hiList = append(hiList, HostInfo{
				IP:          hostIP,
				Unavailable: true,
			})
		}
	}

	sort.Slice(hiList, func(i, j int) bool {
		return hiList[i].IP < hiList[j].IP
	})

	c.JSON(http.StatusOK, hiList)
}

func (s *Service) fetchAllInstanceHostsMap() (map[string]struct{}, error) {
	allHosts := make(map[string]struct{})
	pdInfo, err := topology.FetchPDTopology(s.params.PDClient)
	if err != nil {
		return nil, err
	}
	for _, i := range pdInfo {
		allHosts[i.IP] = struct{}{}
	}

	tikvInfo, tiFlashInfo, err := topology.FetchStoreTopology(s.params.PDClient)
	if err != nil {
		return nil, err
	}
	for _, i := range tikvInfo {
		allHosts[i.IP] = struct{}{}
	}
	for _, i := range tiFlashInfo {
		allHosts[i.IP] = struct{}{}
	}

	tidbInfo, err := topology.FetchTiDBTopology(s.lifecycleCtx, s.params.EtcdClient)
	if err != nil {
		return nil, err
	}
	for _, i := range tidbInfo {
		allHosts[i.IP] = struct{}{}
	}

	return allHosts, nil
}