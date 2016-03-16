from __future__ import print_function

import json
import boto3
import httplib
import re


###### Configuration

ecs_clusters = []
check_health = True
check_health_path = '/health'

####################

print('Loading function')

route53 = boto3.client('route53')
ecs = boto3.client('ecs')
ec2 = boto3.client('ec2')
response = route53.list_hosted_zones_by_name(DNSName='servicediscovery.internal')
if len(response['HostedZones']) == 0:
    raise Exception('Zone not found')
hostedZoneId = response['HostedZones'][0]['Id']

def get_ip_port(rr):
    ip = rr['Value'].split(' ')[3]
    m = re.search(r"\d+-\d+-\d+-\d+", ip)
    if m:
        ip = m.group(0).replace('-','.')
        port = rr['Value'].split(' ')[2]
        return [ip, port]
    return [None, None]
    
def check_health(ip, port):
    try:
        conn = httplib.HTTPConnection(ip, int(port), timeout=2)
        conn.request("GET", check_health_path)
        r1 = conn.getresponse()
        if r1.status != 200:
            return "ERROR"
    except:
        return "ERROR"

def search_ecs_instance(ip, list_ec2_instances):
    for ec2Instance in list_ec2_instances:
        if list_ec2_instances[ec2Instance]['privateIP'] == ip:
            return list_ec2_instances[ec2Instance]['instanceArn']
    
def search_task(port, ec2Instance, service, list_tasks):
    for task in list_tasks:
        if list_tasks[task]['instance'] == ec2Instance and list_tasks[task]['port'] == port and list_tasks[task]['service'] == service:
            return task
        
def search_ecs_task(ip, port, service, ecs_data):
    ec2Instance = search_ecs_instance(ip, ecs_data['ec2Instances'])
    if ec2Instance != None:
        task = search_task(port, ec2Instance, service, ecs_data['tasks'])
        if task != None:
            return task
    
def delete_route53_record(record):
    route53.change_resource_record_sets(
        HostedZoneId=hostedZoneId,
        ChangeBatch={
            'Comment': 'Service Discovery Health Check failed',
            'Changes': [
                {
                    'Action': 'DELETE',
                    'ResourceRecordSet': record
                }
            ]
        })
        
def process_records(response, ecs_data):
    for record in response['ResourceRecordSets']:
        if record['Type'] == 'SRV':
            for rr in record['ResourceRecords']:
                [ip, port] = get_ip_port(rr)
                if ip != None:
                    task=search_ecs_task(ip, int(port), record['Name'].split('.')[0], ecs_data)
                    if task == None:
                        delete_route53_record(record)
                        print("Record %s deleted" % rr)
                        break
                        
                    if check_health:
                        result = "Initial"
                        retries = 3
                        while retries > 0 and result != None:
                            result = check_health(ip, port)
                            retries -= 1
                        if result != None:
                            delete_route53_record(record)
                            print("Record %s deleted" % rr)
                            if task != None:
                                ecs.stop_task(
                                    cluster=ecs_data['instanceArns'][ecs_data['tasks'][task]['instance']]['cluster'],
                                    task=task,
                                    reason='Service Discovery Health Check failed'
                                )
                                print("Task %s stopped" % task)
                    
    
    if response['IsTruncated']:
        if 'NextRecordIdentifier' in response.keys():
            new_response = route53.list_resource_record_sets(
                HostedZoneId=hostedZoneId,
                StartRecordName=response['NextRecordName'],
                StartRecordType=response['NextRecordType'],
                StartRecordIdentifier=response['NextRecordIdentifier'])
        else:
            new_response = route53.list_resource_record_sets(
                HostedZoneId=hostedZoneId,
                StartRecordName=response['NextRecordName'],
                StartRecordType=response['NextRecordType'])
        process_records(new_response)
    
def get_ecs_data():
    for cluster_name in ecs_clusters:
        response = ecs.list_container_instances(cluster=cluster_name)
        list_instance_arns = {}
        for instance_arn in response['containerInstanceArns']:
            list_instance_arns[instance_arn] = {'cluster': cluster_name}
        if len(list_instance_arns.keys()) > 0:
            response = ecs.describe_container_instances(
                cluster=cluster_name,
                containerInstances=list_instance_arns.keys())
            list_ec2_instances = {}
            for instance in response['containerInstances']:
                list_ec2_instances[instance['ec2InstanceId']] = {'instanceArn': instance['containerInstanceArn']}
                list_instance_arns[instance['containerInstanceArn']]['instanceId'] = instance['ec2InstanceId']
            if len(list_ec2_instances.keys()) > 0:
                response = ec2.describe_instances(InstanceIds=list_ec2_instances.keys())
                for reservation in response['Reservations']:
                    for instance in reservation['Instances']:
                        list_ec2_instances[instance['InstanceId']]['privateIP'] = instance['PrivateIpAddress']
        
        response = ecs.list_tasks(cluster=cluster_name, desiredStatus='RUNNING')
        list_tasks = {}
        for task in response['taskArns']:
            list_tasks[task] = {}
        if len(list_tasks.keys()) > 0:
            response = ecs.describe_tasks(cluster = cluster_name, tasks = list_tasks.keys())
            for task in response['tasks']:
                list_tasks[task['taskArn']]['instance'] = task['containerInstanceArn']
                for container in task['containers']:
                    for network in container['networkBindings']:
                        list_tasks[task['taskArn']]['port'] = network['hostPort']
                for containerOverride in task['overrides']['containerOverrides']:
                    list_tasks[task['taskArn']]['service'] = containerOverride['name']
                    
        return {'instanceArns': list_instance_arns, 'ec2Instances': list_ec2_instances, 'tasks': list_tasks}
        
def lambda_handler(event, context):
    #print('Starting')
    
    if len(ecs_clusters) == 0:
        response = ecs.list_clusters()
        for cluster in response['clusterArns']:
            ecs_clusters.append(cluster)

    #print (ecs_clusters)
    response = route53.list_resource_record_sets(HostedZoneId=hostedZoneId)
    
    ecs_data = get_ecs_data()
    #print(ecs_data)
    
    process_records(response, ecs_data)

    return 'Service Discovery Health Check finished'
