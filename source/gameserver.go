/*
Copyright 2021 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package source

import (
	"context"
	"fmt"
	"strings"
	"time"

	agonesv1 "agones.dev/agones/pkg/apis/agones/v1"
	agonesv1client "agones.dev/agones/pkg/client/clientset/versioned"
	agonesv1informerfactory "agones.dev/agones/pkg/client/informers/externalversions"
	agonesv1informers "agones.dev/agones/pkg/client/informers/externalversions/agones/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"

	"sigs.k8s.io/external-dns/endpoint"
)

type gameServerSource struct {
	namespace          string
	gameServerInformer agonesv1informers.GameServerInformer
}

// NewPodSource creates a new podSource with the given config.
func NewGameServerSource(agonesClient agonesv1client.Interface, namespace string) (Source, error) {
	agonesInformerFactory := agonesv1informerfactory.NewSharedInformerFactoryWithOptions(agonesClient, 0, agonesv1informerfactory.WithNamespace(namespace))
	gameServerInformer := agonesInformerFactory.Agones().V1().GameServers()

	gameServerInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {},
		},
	)

	agonesInformerFactory.Start(wait.NeverStop)

	// wait for the local cache to be populated.
	err := poll(time.Second, 60*time.Second, func() (bool, error) {
		return gameServerInformer.Informer().HasSynced(), nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to sync cache: %v", err)
	}

	return &gameServerSource{
		gameServerInformer: gameServerInformer,
		namespace:          namespace,
	}, nil
}

func (gss *gameServerSource) AddEventHandler(ctx context.Context, handler func()) {
}

func (gss *gameServerSource) Endpoints(ctx context.Context) ([]*endpoint.Endpoint, error) {
	gameServers, err := gss.gameServerInformer.Lister().GameServers(gss.namespace).List(labels.Everything())
	if err != nil {
		return nil, err
	}

	endpoints := []*endpoint.Endpoint{}
	for _, gs := range gameServers {
		if domain, ok := gs.Annotations[hostnameAnnotationKey]; ok && !isBeforePodCreated(gs) {
			address := gs.Status.Address

			dnsName := fmt.Sprintf("%s.%s", gs.Name, domain)

			endpoints = append(endpoints, endpoint.NewEndpoint(dnsName, endpoint.RecordTypeA, address))

			if service, ok := gs.Annotations[gameserverServiceNameKey]; ok {
				var protocol string

				if p, ok := gs.Annotations[gameserverProtocolKey]; ok {
					protocol = p
				} else {
					protocol = strings.ToLower(string(gs.Spec.Ports[0].Protocol))
				}

				port := gs.Status.Ports[0].Port

				endpoints = append(endpoints, endpoint.NewEndpoint(
					newSrvDNSName(service, protocol, dnsName),
					endpoint.RecordTypeSRV,
					newSrvTargetName(port, dnsName)))
			}
		}
	}
	return endpoints, nil
}

func newSrvDNSName(serviceName string, protocol string, hostname string) string {
	return fmt.Sprintf("_%s._%s.%s", serviceName, protocol, hostname)
}

func newSrvTargetName(port int32, targetName string) string {
	return fmt.Sprintf("0 50 %d %s", port, targetName)
}

func isBeforePodCreated(gs *agonesv1.GameServer) bool {
	state := gs.Status.State
	return state == agonesv1.GameServerStatePortAllocation || state == agonesv1.GameServerStateCreating || state == agonesv1.GameServerStateStarting
}
