package main

import (
	"context"
	"flag"
	"log"
	"sync"
	"time"

	auth "bitbucket.ema.emoneyadvisor.com/cp/auth"
	dnsv1 "bitbucket.ema.emoneyadvisor.com/cp/lemondns/lib/go/v1"
	grpc "google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	v1beta1 "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	watch "k8s.io/apimachinery/pkg/watch"
	kubernetes "k8s.io/client-go/kubernetes"
)

//FIXME make configurable
//FIXME This should be done from pods not nodes
// k8s-app: traefik-ingress-lb
const loadbalancerLabel = "net=loadbalancer"

type ingressWatchService struct {
	nodeEventMutex *sync.Mutex
	nodeAddresses  map[string]string
	//GRPC
	dnsClient dnsv1.DNSServiceClient
	conn      *grpc.ClientConn
	//Kubernetes
	client *kubernetes.Clientset
}

// NewIngressWatchService - creates a service with established long lived connections
//  Consumers are expected to close this service when done
func NewIngressWatchService(grpcConn string) *ingressWatchService {
	var ret ingressWatchService
	ret.nodeEventMutex = &sync.Mutex{}
	ret.nodeAddresses = make(map[string]string)

	conn, err := grpc.Dial(
		grpcConn,
		grpc.WithInsecure(),
		grpc.WithBlock(),
		grpc.WithTimeout(1*time.Minute))
	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	ret.conn = conn

	ret.dnsClient = dnsv1.NewDNSServiceClient(ret.conn)

	//Authenticate to the Control Plane or die
	client, err := auth.InClusterClient()
	if err != nil {
		log.Fatalln("In cluster client auth failed: ", err)
	}
	ret.client = client
	return &ret
}

func (s *ingressWatchService) Close() {
	s.conn.Close()
}

func (s *ingressWatchService) getLoadBalancerAddresses() (addresses []string) {
	s.nodeEventMutex.Lock()
	defer s.nodeEventMutex.Unlock()

	for _, address := range s.nodeAddresses {
		addresses = append(addresses, address)
	}
	return addresses
}

// putHostToLoadBalancers - Replace Operation - put an A record for each loadbalancer internalIP
//  to DNS for the provided host
func (s *ingressWatchService) putHostToLoadBalancers(host string) {
	lbAddresses := s.getLoadBalancerAddresses()
	for index, addr := range lbAddresses {
		var err error
		if index == 0 {
			_, err = s.dnsClient.PutRecord(context.Background(),
				&dnsv1.Entry{
					Address: addr,
					Name:    host,
				})
		} else {
			_, err = s.dnsClient.PostRecord(context.Background(),
				&dnsv1.Entry{
					Address: addr,
					Name:    host,
				})
		}
		if err != nil {
			log.Println("Error sending DNS Put request: ", err)
		}
	}
}

// putHostToLoadBalancers - Update Operation - post an A record for each loadbalancer internalIP
//  to DNS for the provided host
func (s *ingressWatchService) postHostToLoadBalancers(host string) {
	lbAddresses := s.getLoadBalancerAddresses()
	for _, addr := range lbAddresses {
		_, err := s.dnsClient.PostRecord(context.Background(),
			&dnsv1.Entry{
				Address: addr,
				Name:    host,
			})
		if err != nil {
			log.Println("Error sending DNS Post request: ", err)
		}
	}
}

// deleteHost - deletes all A records for the given host
//  FIXME:  This deletes the A record on the deletion of an ingress with that host.  If multiple ingresses
//    contain that host it will be deleted anyway and break the other ingresses
func (s *ingressWatchService) deleteHost(host string) {
	lbAddresses := s.getLoadBalancerAddresses()
	for _, addr := range lbAddresses {
		_, err := s.dnsClient.DeleteRecord(context.Background(),
			&dnsv1.Entry{
				Name:    host,
				Address: addr,
			})
		if err != nil {
			log.Println("Error sending DNS Delete request: ", err)
		}
	}
}

// Watch ingress events
func (s *ingressWatchService) watchIngressForever(namespace string) {
	ingWatch, err := s.client.Extensions().Ingresses(namespace).Watch(metav1.ListOptions{})
	if err != nil || ingWatch == nil {
		log.Printf("Could not watch ingress in namespace: %s", namespace)
		log.Println(err)
		return
	}
	channel := ingWatch.ResultChan()

	log.Printf("Watching Inresses In Namespace: %s", namespace)
	for {
		event, ok := <-channel
		if ok {
			switch event.Type {
			case watch.Added:
				log.Printf("Ingress Added in namespace %s!", namespace)
				if ingress, ok := event.Object.(*v1beta1.Ingress); ok {
					for _, rule := range ingress.Spec.Rules {
						s.putHostToLoadBalancers(rule.Host)
					}
				} else {
					log.Println("Runtime Object not of type Ingress!")
				}

			case watch.Modified:
				log.Printf("Ingress Modified in namespace %s!", namespace)
				if ingress, ok := event.Object.(*v1beta1.Ingress); ok {
					for _, rule := range ingress.Spec.Rules {
						s.putHostToLoadBalancers(rule.Host)
					}
				} else {
					log.Println("Runtime Object not of type Ingress!")
				}
			case watch.Deleted:
				if ingress, ok := event.Object.(*v1beta1.Ingress); ok {
					for _, rule := range ingress.Spec.Rules {
						s.deleteHost(rule.Host)
					}
				} else {
					log.Println("Runtime Object not of type Ingress!")
				}
			case watch.Error:
				log.Println("Namespace Watch Error: ", event.Object)
			}
		} else {
			log.Printf("Ingress Channel %s Closed!", namespace)
			break //Forever
		}
	}
}

func (s *ingressWatchService) updateAllIngresses() error {
	namespaceList, err := s.client.Core().Namespaces().List(metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, namespace := range namespaceList.Items {
		ingressList, err := s.client.Extensions().Ingresses(namespace.Name).List(metav1.ListOptions{})
		if err != nil {
			return err
		}
		for _, ingress := range ingressList.Items {
			for _, rule := range ingress.Spec.Rules {
				//Must be put to replace all
				s.putHostToLoadBalancers(rule.Host)
			}
		}
	}
	return nil
}

func (s *ingressWatchService) addNode(node *v1.Node) (success bool) {
	s.nodeEventMutex.Lock()
	defer s.nodeEventMutex.Unlock()
	for _, address := range node.Status.Addresses {
		if address.Type == v1.NodeInternalIP {
			s.nodeAddresses[node.Name] = address.Address
			return true
		}
	}
	return false
}

func (s *ingressWatchService) removeNode(node *v1.Node) {
	s.nodeEventMutex.Lock()
	defer s.nodeEventMutex.Unlock()
	delete(s.nodeAddresses, node.Name)
}

//watch node events
//TODO: Untested
func (s *ingressWatchService) watchNodesForever() {
	nodeWatch, err := s.client.Core().Nodes().Watch(metav1.ListOptions{
		LabelSelector: loadbalancerLabel,
	})
	if err != nil || nodeWatch == nil {
		log.Printf("Could not watch nodes!")
		log.Println(err)
		return
	}
	channel := nodeWatch.ResultChan()
	log.Printf("Watching Nodes In Cluster")
	for {
		event, ok := <-channel
		if ok {
			switch event.Type {
			case watch.Added:
				//Add the node to the cache
				//FIXME:  This is not good enough.  We need to wait for the readiness probe
				var updated bool
				if node, ok := event.Object.(*v1.Node); ok {
					updated = s.addNode(node)
				}
				if !updated {
					log.Println("Could not update all ingresses after node creation!")
				} else {
					err := s.updateAllIngresses()
					if err != nil {
						log.Println("Could not update all ingresses:", err)
					}
				}
			case watch.Modified:
				//NOOP
			case watch.Deleted:
				if node, ok := event.Object.(*v1.Node); ok {
					s.removeNode(node)
				}
				err := s.updateAllIngresses()
				if err != nil {
					log.Println("Could not update all ingresses after node creation!")
				}
			case watch.Error:
				log.Println("Node Watch Error: ", event.Object)
			}
		} else {
			log.Println("Node Channel Closed!")
			break
		}
	}
}

// Since ingresses belong to a namespace watch the namespace creation and deletion
func (s *ingressWatchService) watchNamespaceForever() {
	namespaceWatch, err := s.client.Core().Namespaces().Watch(metav1.ListOptions{})
	if err != nil {
		log.Fatalln("Failed to watch namespaces!")
	}
	channel := namespaceWatch.ResultChan()
	for {
		event, ok := <-channel
		if ok {
			switch event.Type {
			case watch.Added:
				log.Println("Namespace Added!")
				if namespace, ok := event.Object.(*v1.Namespace); ok {
					//Background the ingress watching
					go s.watchIngressForever(namespace.Name)
				} else {
					log.Println("Watch object not a namespace!")
				}
			case watch.Modified:
				//NOOP
			case watch.Deleted:
				//Any remaining ingresses will also be deleted
			case watch.Error:
				log.Println("Namespace Watch Error: ", event.Object)
			}
		} else {
			log.Fatalln("Namespace Channel Closed!")
		}
	}
}

func main() {
	address := flag.String("address", "10.99.91.30:50051", "address of the dns server")
	flag.Parse()

	service := NewIngressWatchService(*address)
	go service.watchNodesForever()
	service.watchNamespaceForever()
}
