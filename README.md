# Service Discovery for AWS EC2 Container Service
## Goals
This project has been created to facilitate the creation of MicroServices on top of AWS ECS.

Some of the tenets are:

* Start services in any order
* Stop services with confidence
* Automatically register/de-register services when started/stopped
* Load balance access to services

## Installation
You need a private hosted zone in Route53 to register all the containers for each service. You can create the hosted zone using the CloudFormation template "privatedns.cform".

To create an ECS Cluster with all the required configuration you can use the CloudFormation template "environment.cform". This template creates an Autoscaling Configuration and Group, and an ECS cluster.

## Usage
Once the cluster is created, you can start launching tasks and services into the ECS Cluster. For each task you want to register as a MicroService, you should specify a Name to the ContainerDefinition, this name is going to be the name of your service.

You should publish the port of the container using the portMappings properties. When you publish the port I recommend you to not specify the containerPort and leave it to be assigned randomly, this way you could have multiple containers of the same service running in the same server.

When the service starts, and the container is launched in one of the servers, the ecssd agent is going to register a new DNS record automatically, with the name <serviceName>.servicediscovery.internal and the type SRV.

You can use this name to access the service from your consumers, Route53 is going to balance the requests between your different containers for the same service. For example in go you can use:

```golang
func getServiceEnpoint() (string, error) {
	_, addrs, err := net.LookupSRV("", "", "serviceName.servicediscovery.internal")
	if err != nil {
		return "", err
	}
	for _, addr := range addrs {
		return strings.TrimRight(addr.Target, ".") + ":" + strconv.Itoa(int(addr.Port)), nil
	}
	return "", error.New("No record found")
}
```
