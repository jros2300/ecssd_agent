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

You should create a Lambda function to monitor the services, in case a host fails completely and the agent cannot delete the records. You can also use the Lambda function to do HTTP health checks for your containers.

Create a role for the Lambda function, this role should have full access to Route53 (at least to the intenal hosted zone), read only access to ECS and read only access to EC2.

Create a lambda function using the code in lambda_health_check.py, you can modify the parameters in the funtion:

* ecs_clusters: This is an array with all the clusters with the agent installed. You can leave it empty and the function will get the list of clusters from your account.
* check_health: Indicate if you want to do HTTP Health Check to all the containers.
* check_health_path: The path of the Health Check URL in the containers.

You should then schedule the Lambda funtion to run every 5 minutes.

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
