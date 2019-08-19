package service

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/turnerlabs/udeploy/component/app"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/applicationautoscaling"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/turnerlabs/udeploy/component/integration/aws/task"
)

// Populate ...
func Populate(instances map[string]app.Instance, details bool) (map[string]app.Instance, error) {
	session := session.New()

	svc := ecs.New(session)

	resourceIds := []string{}
	for _, i := range instances {
		resourceIds = append(resourceIds, fmt.Sprintf("service/%s/%s", i.Cluster, i.Service))
	}

	ascv := applicationautoscaling.New(session)

	ao, err := ascv.DescribeScalableTargets(&applicationautoscaling.DescribeScalableTargetsInput{
		ServiceNamespace: aws.String("ecs"),
		ResourceIds:      aws.StringSlice(resourceIds),
	})
	if err != nil {
		return instances, err
	}

	instanceChan := make(chan chanModel, len(instances))

	for name, instance := range instances {

		go func(innerName string, innerInstance app.Instance, innerSvc *ecs.ECS) {
			state := app.State{}

			td, svcs, err := getServiceInfo(innerInstance, innerSvc)
			if err != nil {
				state.Error = err
			} else {
				innerInstance.Task.Definition = app.DefinitionFrom(td, innerInstance.Task.ImageTagEx)

				state.IsPending = isPending(svcs)
				state.IsRunning = isRunning(svcs)
				state.Version = innerInstance.FormatVersion()
				state.Error = nil

				if state.IsPending {
					state.Error = getError(svc, innerInstance)
				}

				for _, t := range ao.ScalableTargets {
					if *t.ResourceId == fmt.Sprintf("service/%s/%s", innerInstance.Cluster, innerInstance.Service) {
						state.DesiredCount = *t.MinCapacity
					}
				}

				if details {
					innerInstance.Task.TasksInfo, err = task.GetTasksInfo(innerInstance, innerSvc)
					if err != nil {
						state.Error = err
					}
				}

				region, err := getRegion(*td.TaskDefinitionArn)
				if err == nil {
					innerInstance.Links = append(innerInstance.Links, app.Link{
						Generated:   true,
						Description: "AWS Console Service Logs",
						Name:        "logs",
						URL: fmt.Sprintf("https://console.aws.amazon.com/ecs/home?region=%s#/clusters/%s/services/%s/logs",
							region, innerInstance.Cluster, innerInstance.Service),
					})
				}

			}

			innerInstance.SetState(state)

			instanceChan <- chanModel{
				name:     innerName,
				instance: innerInstance,
			}

		}(name, instance, svc)
	}

	for respCount := 1; respCount <= len(instances); respCount++ {
		i := <-instanceChan

		instances[i.name] = i.instance

		if respCount == len(instances) {
			close(instanceChan)
		}
	}

	return instances, nil
}

type chanModel struct {
	name     string
	instance app.Instance
}

func getRegion(arn string) (string, error) {
	tag := regexp.MustCompile("([a-z]{2}-[a-z]*-[0-9]{1})")

	matches := tag.FindStringSubmatch(arn)
	if matches == nil {
		return "", errors.New("failed to get region")
	}

	if len(matches) >= 2 && len(matches[1]) > 0 {
		return matches[1], nil
	}

	return "", errors.New("failed to get region")
}

func getError(svc *ecs.ECS, inst app.Instance) error {

	tasks, err := getTaskDetails(svc, inst, []*ecs.Task{}, "")
	if err != nil {
		return err
	}

	if count, err := getTaskError(tasks); err != nil {
		return fmt.Errorf("%d failed task(s) (%s)", count, err)
	}

	return nil
}

func getTaskDetails(svc *ecs.ECS, inst app.Instance, tasks []*ecs.Task, nextToken string) ([]*ecs.Task, error) {
	input := &ecs.ListTasksInput{
		Cluster:       aws.String(inst.Cluster),
		ServiceName:   aws.String(inst.Service),
		DesiredStatus: aws.String("STOPPED"),
	}

	if len(nextToken) > 0 {
		input.SetNextToken(nextToken)
	}

	stoppedTasks, err := svc.ListTasks(input)
	if err != nil {
		return nil, err
	}

	if len(stoppedTasks.TaskArns) > 0 {
		taskDetails, err := svc.DescribeTasks(&ecs.DescribeTasksInput{
			Cluster: aws.String(inst.Cluster),
			Tasks:   stoppedTasks.TaskArns,
		})
		if err != nil {
			return nil, err
		}

		tasks = append(tasks, taskDetails.Tasks...)
	}

	if stoppedTasks.NextToken == nil || len(*stoppedTasks.NextToken) == 0 {
		return tasks, nil
	}

	return getTaskDetails(svc, inst, tasks, nextToken)
}

func getTaskError(tasks []*ecs.Task) (int, error) {
	var reason error
	count := 0

	for _, t := range tasks {
		if t.StopCode != nil && t.StoppedReason != nil {
			if *t.StopCode != ecs.TaskStopCodeUserInitiated {
				reason = errors.New(*t.StoppedReason)
				count++
			}
		}
	}

	return count, reason
}

func isPending(svc *ecs.Service) bool {
	return (len(svc.Deployments) > 1 && *svc.DesiredCount > 0) || *svc.PendingCount > 0 || (*svc.DesiredCount != *svc.RunningCount)
}

func isRunning(svc *ecs.Service) bool {
	return *svc.RunningCount > 0 && *svc.RunningCount >= *svc.DesiredCount
}

func getServiceInfo(instance app.Instance, svc *ecs.ECS) (*ecs.TaskDefinition, *ecs.Service, error) {
	o, err := svc.DescribeServices(&ecs.DescribeServicesInput{
		Cluster:  aws.String(instance.Cluster),
		Services: aws.StringSlice([]string{instance.Service}),
	})
	if err != nil {
		return nil, nil, err
	}

	if len(o.Services) == 0 {
		return nil, nil, fmt.Errorf("service not found with name %s", instance.Service)
	}

	if len(o.Services) > 1 {
		return nil, nil, fmt.Errorf("too many services returned for %s", instance.Service)
	}

	tdo, err := svc.DescribeTaskDefinition(&ecs.DescribeTaskDefinitionInput{
		TaskDefinition: aws.String(*o.Services[0].TaskDefinition),
	})
	if err != nil {
		return nil, nil, err
	}

	return tdo.TaskDefinition, o.Services[0], nil
}
