package containrunner

import (
	"fmt"
	"github.com/coreos/go-etcd/etcd"
	"github.com/op/go-logging"
	"os"
	"strings"
	"time"
)

var log = logging.MustGetLogger("containrunner")

type Containrunner struct {
	Tags              []string
	EtcdEndpoints     []string
	exitChannel       chan bool
	MachineAddress    string
	CheckIntervalInMs int
	HAProxySettings   HAProxySettings
	EtcdBasePath      string
}

type RuntimeConfiguration struct {
	MachineConfiguration MachineConfiguration

	// First string is service name, second string is backend host:port
	ServiceBackends map[string]map[string]*EndpointInfo `json:"-"`

	// Locally required service groups in haproxy, should be refactored away from this struct
	LocallyRequiredServices map[string]map[string]*EndpointInfo `json:"-" DeepEqual:"skip"`
}

func MainExecutionLoop(exitChannel chan bool, containrunner Containrunner) {

	log.Info(LogString("MainExecutionLoop started"))

	etcdClient := GetEtcdClient(containrunner.EtcdEndpoints)
	docker := GetDockerClient()
	var checkEngine CheckEngine
	checkEngine.Start(4, &ConfigResultEtcdPublisher{10, containrunner.EtcdBasePath, containrunner.EtcdEndpoints, nil}, containrunner.MachineAddress, containrunner.CheckIntervalInMs)

	var currentConfiguration RuntimeConfiguration
	var newConfiguration RuntimeConfiguration
	var err error

	var webserver Webserver
	webserver.Containrunner = &containrunner
	webserver.Start()

	somethingChanged := false

	quit := false
	var lastConverge time.Time
	for !quit {
		select {
		case val := <-exitChannel:
			if val == true {
				log.Info(LogString("MainExecutionLoop stopping"))
				quit = true
				checkEngine.Stop()
				//etcd.Close()
				//docker.Close()
				exitChannel <- true
			}
		default:
			somethingChanged = false

			newConfiguration.MachineConfiguration, err = containrunner.GetMachineConfigurationByTags(etcdClient, containrunner.Tags)
			if err != nil {
				if strings.HasPrefix(err.Error(), "100:") {
					log.Info(LogString("Error:" + err.Error()))
				} else if strings.HasPrefix(err.Error(), "50") {
					log.Info(LogString("Error:" + err.Error()))
					log.Info(LogString("Reconnecting to etcd..."))
					etcdClient = GetEtcdClient(containrunner.EtcdEndpoints)

				} else {
					panic(err)
				}
				log.Info(LogString("Sleeping for 5 seconds due to error on GetMachineConfigurationByTags"))
				time.Sleep(time.Second * 5)
				continue
			}

			newConfiguration.ServiceBackends, err = containrunner.GetAllServiceEndpoints()

			if !DeepEqual(currentConfiguration.MachineConfiguration, newConfiguration.MachineConfiguration) {
				log.Info(LogString("New Machine Configuration. Pushing changes to check engine"))

				somethingChanged = true
				ConvergeContainers(newConfiguration.MachineConfiguration, docker)

				// This must be done after the containers have been converged so that the Check Engine
				// can report the correct container revision
				checkEngine.PushNewConfiguration(newConfiguration.MachineConfiguration)

				lastConverge = time.Now()

			} else if time.Now().Sub(lastConverge) > time.Second*10 {
				ConvergeContainers(newConfiguration.MachineConfiguration, docker)
				lastConverge = time.Now()

			}

			if !DeepEqual(currentConfiguration, newConfiguration) && newConfiguration.MachineConfiguration.HAProxyConfiguration != nil {
				somethingChanged = true

				if !DeepEqual(currentConfiguration.MachineConfiguration, newConfiguration.MachineConfiguration) {
					fmt.Fprintf(os.Stderr, "Difference found in MachineConfiguration\n")
					if !DeepEqual(currentConfiguration.MachineConfiguration.HAProxyConfiguration, newConfiguration.MachineConfiguration.HAProxyConfiguration) {
						fmt.Fprintf(os.Stderr, "Difference found in MachineConfiguration.HAProxyConfiguration\n")
					}

					if !DeepEqual(currentConfiguration.MachineConfiguration.Services, newConfiguration.MachineConfiguration.Services) {
						fmt.Fprintf(os.Stderr, "Difference found in MachineConfiguration.Services\n")
					}

				}
				if !DeepEqual(currentConfiguration.ServiceBackends, newConfiguration.ServiceBackends) {
					fmt.Fprintf(os.Stderr, "Difference found in ServiceBackends\n")

					for service, _ := range currentConfiguration.ServiceBackends {
						_, found := newConfiguration.ServiceBackends[service]
						if found {
							if !DeepEqual(currentConfiguration.ServiceBackends[service], newConfiguration.ServiceBackends[service]) {
								fmt.Fprintf(os.Stderr, "Service %s differs between old and new (%d vs %d items)\n", service, len(currentConfiguration.ServiceBackends[service]), len(newConfiguration.ServiceBackends[service]))
							}
						} else {
							fmt.Fprintf(os.Stderr, "Service %s not found in new ServiceBackends\n", service)
						}
					}
				}

				//bytes, _ := json.MarshalIndent(currentConfiguration, "", "    ")
				//fmt.Fprintf(os.Stderr, "Old configuration: %s\n", string(bytes))
				//bytes, _ = json.MarshalIndent(newConfiguration, "", "    ")
				//fmt.Fprintf(os.Stderr, "New configuration: %s\n", string(bytes))

				go func(containrunner *Containrunner, runtimeConfiguration RuntimeConfiguration, oldConfiguration RuntimeConfiguration) {
					containrunner.HAProxySettings.ConvergeHAProxy(&runtimeConfiguration, &oldConfiguration)
				}(&containrunner, newConfiguration, currentConfiguration)

			}

			if somethingChanged {
				currentConfiguration = newConfiguration
			}

		}

		time.Sleep(time.Second * 2)
		webserver.Keepalive()

	}
}

func (s *Containrunner) Start() {
	log.Info(LogString("Starting Containrunner..."))

	s.exitChannel = make(chan bool, 1)

	go MainExecutionLoop(s.exitChannel, *s)
}

func (s *Containrunner) Wait() {
	<-s.exitChannel
}

func GetEtcdClient(endpoints []string) *etcd.Client {
	e := etcd.NewClient(endpoints)
	return e
}

/*
func LogSocketCount(pos string) {
	out, err := exec.Command("netstat", "-np").Output()
	if err != nil {
		log.Fatal(err)
	}

	m := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Index(line, "orbitctl") != -1 {
			m++
		}
	}
	fmt.Printf("***** %d open sockets at pos %s\n", m, pos)
}*/
