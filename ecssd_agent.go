package main

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/fsouza/go-dockerclient"
	"os"
	"time"
)

const workerTimeout = 60 * time.Second

type Handler interface {
	Handle(*docker.APIEvents) error
}

type EventRouter struct {
	handlers      map[string][]Handler
	dockerClient  *docker.Client
	listener      chan *docker.APIEvents
	workers       chan *worker
	workerTimeout time.Duration
}

func NewEventRouter(bufferSize int, workerPoolSize int, dockerClient *docker.Client,
	handlers map[string][]Handler) (*EventRouter, error) {
	workers := make(chan *worker, workerPoolSize)
	for i := 0; i < workerPoolSize; i++ {
		workers <- &worker{}
	}

	eventRouter := &EventRouter{
		handlers:      handlers,
		dockerClient:  dockerClient,
		listener:      make(chan *docker.APIEvents, bufferSize),
		workers:       workers,
		workerTimeout: workerTimeout,
	}

	return eventRouter, nil
}

func (e *EventRouter) Start() error {
	log.Info("Starting event router.")
	go e.routeEvents()
	if err := e.dockerClient.AddEventListener(e.listener); err != nil {
		return err
	}
	return nil
}

func (e *EventRouter) Stop() error {
	if e.listener == nil {
		return nil
	}
	if err := e.dockerClient.RemoveEventListener(e.listener); err != nil {
		return err
	}
	return nil
}

func (e *EventRouter) routeEvents() {
	for {
		event := <-e.listener
		timer := time.NewTimer(e.workerTimeout)
		gotWorker := false
		for !gotWorker {
			select {
			case w := <-e.workers:
				go w.doWork(event, e)
				gotWorker = true
			case <-timer.C:
				log.Infof("Timed out waiting for worker. Re-initializing wait.")
			}
		}
	}
}

type worker struct{}

func (w *worker) doWork(event *docker.APIEvents, e *EventRouter) {
	defer func() { e.workers <- w }()
	if handlers, ok := e.handlers[event.Status]; ok {
		log.Infof("Processing event: %#v", event)
		for _, handler := range handlers {
			if err := handler.Handle(event); err != nil {
				log.Errorf("Error processing event %#v. Error: %v", event, err)
			}
		}
	} //else {
	//	log.Infof("No processing event: %#v", event)
	//}
}

type dockerHandler struct {
	handlerFunc func(event *docker.APIEvents) error
}

func (th *dockerHandler) Handle(event *docker.APIEvents) error {
	return th.handlerFunc(event)
}

type Config struct {
	EcsCluster string
	Region     string
}

func testError(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func testErrorNoFatal(err error) {
	if err != nil {
		log.Error(err)
	}
}

type TopTasks struct {
	Tasks []TaskInfo
}

type TaskInfo struct {
	Arn           string
	DesiredStatus string
	KnownStatus   string
	Family        string
	Version       string
	Containers    []ContainerInfo
}

type ContainerInfo struct {
	DockerId   string
	DockerName string
	Name       string
}

func createDNSRecord(serviceName string, dockerId string, port string) error {
	r53 := route53.New(session.New())
	hostname, _ := os.Hostname()
	params := &route53.ChangeResourceRecordSetsInput{
		ChangeBatch: &route53.ChangeBatch{
			Changes: []*route53.Change{
				{
					Action: aws.String(route53.ChangeActionCreate),
					ResourceRecordSet: &route53.ResourceRecordSet{
						Name: aws.String(serviceName + ".servicediscovery.internal"),
						Type: aws.String(route53.RRTypeSrv),
						ResourceRecords: []*route53.ResourceRecord{
							{
								Value: aws.String("1 1 " + port + " " + hostname + ".compute.internal."),
							},
						},
						SetIdentifier: aws.String(dockerId),
						TTL:           aws.Int64(0),
						Weight:        aws.Int64(1),
					},
				},
			},
			Comment: aws.String("Service Discovery Created Record"),
		},
		HostedZoneId: aws.String("Z2732IVNA0Y7I1"),
	}
	_, err := r53.ChangeResourceRecordSets(params)
	testErrorNoFatal(err)
	fmt.Println("Record " + serviceName + ".servicediscovery.internal created (1 1 " + port + " " + hostname + ".compute.internal.)")
	return err
}

func deleteDNSRecord(serviceName string, dockerId string) error {
	var err error
	r53 := route53.New(session.New())
	paramsList := &route53.ListResourceRecordSetsInput{
		HostedZoneId:          aws.String("Z2732IVNA0Y7I1"), // Required
		MaxItems:              aws.String("10"),
		StartRecordIdentifier: aws.String(dockerId),
		StartRecordName:       aws.String(serviceName + ".servicediscovery.internal"),
		StartRecordType:       aws.String(route53.RRTypeSrv),
	}
	resp, err := r53.ListResourceRecordSets(paramsList)
	testErrorNoFatal(err)
	if err != nil {
		return err
	}
	srvValue := ""
	for _, rrset := range resp.ResourceRecordSets {
		if *rrset.SetIdentifier == dockerId {
			for _, rrecords := range rrset.ResourceRecords {
				srvValue = *rrecords.Value
			}
		}
	}
	if srvValue == "" {
		log.Error("Route53 Record doesn't exist")
		return nil
	}

	params := &route53.ChangeResourceRecordSetsInput{
		ChangeBatch: &route53.ChangeBatch{
			Changes: []*route53.Change{
				{
					Action: aws.String(route53.ChangeActionDelete),
					ResourceRecordSet: &route53.ResourceRecordSet{
						Name: aws.String(serviceName + ".servicediscovery.internal"),
						Type: aws.String(route53.RRTypeSrv),
						ResourceRecords: []*route53.ResourceRecord{
							{
								Value: aws.String(srvValue),
							},
						},
						SetIdentifier: aws.String(dockerId),
						TTL:           aws.Int64(0),
						Weight:        aws.Int64(1),
					},
				},
			},
		},
		HostedZoneId: aws.String("Z2732IVNA0Y7I1"),
	}
	_, err = r53.ChangeResourceRecordSets(params)
	testErrorNoFatal(err)
	fmt.Println("Record " + serviceName + ".servicediscovery.internal deleted ( " + srvValue + ")")
	return err
}

var dockerClient *docker.Client

func main() {
	var err error
	var sum time.Duration
	endpoint := "unix:///var/run/docker.sock"
	startFn := func(event *docker.APIEvents) error {
		var err error
		container, err := dockerClient.InspectContainer(event.ID)
		testError(err)
		serviceName := container.Config.Labels["com.amazonaws.ecs.container-name"]
		port := ""
		for _, mapping := range container.NetworkSettings.Ports {
			if mapping[0].HostIP == "0.0.0.0" {
				port = mapping[0].HostPort
				break
			}
		}
		if port != "" {
			sum = 1
			for {
				err = createDNSRecord(serviceName, event.ID, port)
				if err == nil {
					break
				}
				if sum > 8 {
					testError(err)
				}
				time.Sleep(sum * time.Second)
				sum += 2
			}
		}
		fmt.Println("Docker " + event.ID + " started")
		return nil
	}

	stopFn := func(event *docker.APIEvents) error {
		var err error
		container, err := dockerClient.InspectContainer(event.ID)
		testError(err)
		serviceName := container.Config.Labels["com.amazonaws.ecs.container-name"]
		sum = 1
		for {
			err = deleteDNSRecord(serviceName, event.ID)
			if err == nil {
				break
			}
			if sum > 8 {
				testError(err)
			}
			time.Sleep(sum * time.Second)
			sum += 2
		}
		fmt.Println("Docker " + event.ID + " stopped")
		return nil
	}

	startHandler := &dockerHandler{
		handlerFunc: startFn,
	}
	stopHandler := &dockerHandler{
		handlerFunc: stopFn,
	}
	handlers := map[string][]Handler{"start": []Handler{startHandler}, "die": []Handler{stopHandler}}

	dockerClient, _ = docker.NewClient(endpoint)
	router, err := NewEventRouter(5, 5, dockerClient, handlers)
	testError(err)
	defer router.Stop()
	router.Start()
	fmt.Println("Waiting events")
	select {}

}
