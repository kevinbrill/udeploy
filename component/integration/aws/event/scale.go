package event

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudwatchevents"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/turnerlabs/udeploy/component/integration/aws/task"
	"github.com/turnerlabs/udeploy/model"
)

func Scale(ctx context.Context, instance model.Instance, desiredCount int64, restart bool) error {
	usr := ctx.Value(model.ContextKey("user")).(model.User)
	evtSvc := cloudwatchevents.New(session.New())
	ecsSvc := ecs.New(session.New())
	input := &cloudwatchevents.ListTargetsByRuleInput{
		Rule: &instance.EventRule,
	}
	targetOutput, err := evtSvc.ListTargetsByRule(input)
	if err != nil {
		return err
	}
	if len(targetOutput.Targets) == 0 {
		return fmt.Errorf("event target not found")
	}

	target := targetOutput.Targets[0]

	if desiredCount == 0 {
		return stopTasks(ctx, instance, target.EcsParameters.TaskDefinitionArn, ecsSvc)
	}

	if restart {
		if err := stopTasks(ctx, instance, target.EcsParameters.TaskDefinitionArn, ecsSvc); err != nil {
			return err
		}
	}

	_, err = ecsSvc.RunTask(&ecs.RunTaskInput{
		Cluster:    &instance.Cluster,
		Count:      &desiredCount,
		LaunchType: target.EcsParameters.LaunchType,
		Group:      target.EcsParameters.Group,
		NetworkConfiguration: &ecs.NetworkConfiguration{
			AwsvpcConfiguration: &ecs.AwsVpcConfiguration{
				AssignPublicIp: target.EcsParameters.NetworkConfiguration.AwsvpcConfiguration.AssignPublicIp,
				SecurityGroups: target.EcsParameters.NetworkConfiguration.AwsvpcConfiguration.SecurityGroups,
				Subnets:        target.EcsParameters.NetworkConfiguration.AwsvpcConfiguration.Subnets,
			},
		},
		TaskDefinition: target.EcsParameters.TaskDefinitionArn,
		StartedBy:      aws.String(usr.Email),
	})
	if err != nil {
		return err
	}

	return nil
}

func stopTasks(ctx context.Context, instance model.Instance, taskArn *string, svc *ecs.ECS) error {
	usr := ctx.Value(model.ContextKey("user")).(model.User)

	tasks, err := task.List(instance, svc, aws.String("RUNNING"))
	if err != nil {
		return err
	}

	reason := fmt.Sprintf("Stopped by %s", usr.Email)
	for _, task := range tasks {
		svc.StopTask(&ecs.StopTaskInput{
			Cluster: &instance.Cluster,
			Reason:  &reason,
			Task:    task.TaskArn,
		})
	}

	return nil
}