package node_cpu_hog

import (
	"math/rand"
	"strconv"
	"time"

	clients "github.com/litmuschaos/litmus-go/pkg/clients"
	"github.com/litmuschaos/litmus-go/pkg/events"
	experimentTypes "github.com/litmuschaos/litmus-go/pkg/generic/node-cpu-hog/types"
	"github.com/litmuschaos/litmus-go/pkg/log"
	"github.com/litmuschaos/litmus-go/pkg/status"
	"github.com/litmuschaos/litmus-go/pkg/types"
	"github.com/litmuschaos/litmus-go/pkg/utils/common"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	apiv1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var err error

// PrepareNodeCPUHog contains prepration steps before chaos injection
func PrepareNodeCPUHog(experimentsDetails *experimentTypes.ExperimentDetails, clients clients.ClientSets, resultDetails *types.ResultDetails, eventsDetails *types.EventDetails, chaosDetails *types.ChaosDetails) error {

	//Select the node name
	appNodeName, err := GetNodeName(experimentsDetails, clients)
	if err != nil {
		return errors.Errorf("Unable to get the node name due to, err: %v", err)
	}

	// When number of cpu cores for hogging is not defined , it will take it from node capacity
	if experimentsDetails.NodeCPUcores == 0 {
		err = SetCPUCapacity(experimentsDetails, appNodeName, clients)
		if err != nil {
			return err
		}
	}

	log.InfoWithValues("[Info]: Details of application under chaos injection", logrus.Fields{
		"NodeName":     appNodeName,
		"NodeCPUcores": experimentsDetails.NodeCPUcores,
	})

	experimentsDetails.RunID = common.GetRunID()

	//Waiting for the ramp time before chaos injection
	if experimentsDetails.RampTime != 0 {
		log.Infof("[Ramp]: Waiting for the %vs ramp time before injecting chaos", strconv.Itoa(experimentsDetails.RampTime))
		common.WaitForDuration(experimentsDetails.RampTime)
	}

	if experimentsDetails.EngineName != "" {
		msg := "Injecting " + experimentsDetails.ExperimentName + " chaos on " + appNodeName + " node"
		types.SetEngineEventAttributes(eventsDetails, types.ChaosInject, msg, "Normal", chaosDetails)
		events.GenerateEvents(eventsDetails, clients, chaosDetails, "ChaosEngine")
	}

	// Creating the helper pod to perform node cpu hog
	err = CreateHelperPod(experimentsDetails, clients, appNodeName)
	if err != nil {
		return errors.Errorf("Unable to create the helper pod, err: %v", err)
	}

	//Checking the status of helper pod
	log.Info("[Status]: Checking the status of the helper pod")
	err = status.CheckApplicationStatus(experimentsDetails.ChaosNamespace, "name="+experimentsDetails.ExperimentName+"-"+experimentsDetails.RunID, experimentsDetails.Timeout, experimentsDetails.Delay, clients)
	if err != nil {
		return errors.Errorf("helper pod is not in running state, err: %v", err)
	}

	// Wait till the completion of helper pod
	log.Infof("[Wait]: Waiting for %vs till the completion of the helper pod", strconv.Itoa(experimentsDetails.ChaosDuration+30))

	podStatus, err := status.WaitForCompletion(experimentsDetails.ChaosNamespace, "name="+experimentsDetails.ExperimentName+"-"+experimentsDetails.RunID, clients, experimentsDetails.ChaosDuration+30, experimentsDetails.ExperimentName)
	if err != nil || podStatus == "Failed" {
		return errors.Errorf("helper pod failed due to, err: %v", err)
	}

	// Checking the status of application node
	log.Info("[Status]: Getting the status of application node")
	err = status.CheckNodeStatus(appNodeName, experimentsDetails.Timeout, experimentsDetails.Delay, clients)
	if err != nil {
		log.Warn("Application node is not in the ready state, you may need to manually recover the node")
	}

	//Deleting the helper pod
	log.Info("[Cleanup]: Deleting the helper pod")
	err = common.DeletePod(experimentsDetails.ExperimentName+"-"+experimentsDetails.RunID, "name="+experimentsDetails.ExperimentName+"-"+experimentsDetails.RunID, experimentsDetails.ChaosNamespace, chaosDetails.Timeout, chaosDetails.Delay, clients)
	if err != nil {
		return errors.Errorf("Unable to delete the helper pod, err: %v", err)
	}

	//Waiting for the ramp time after chaos injection
	if experimentsDetails.RampTime != 0 {
		log.Infof("[Ramp]: Waiting for the %vs ramp time after injecting chaos", strconv.Itoa(experimentsDetails.RampTime))
		common.WaitForDuration(experimentsDetails.RampTime)
	}
	return nil
}

//GetNodeName will select a random replica of application pod and return the node name of that application pod
func GetNodeName(experimentsDetails *experimentTypes.ExperimentDetails, clients clients.ClientSets) (string, error) {
	podList, err := clients.KubeClient.CoreV1().Pods(experimentsDetails.AppNS).List(v1.ListOptions{LabelSelector: experimentsDetails.AppLabel})
	if err != nil || len(podList.Items) == 0 {
		return "", errors.Wrapf(err, "Fail to get the application pod in %v namespace, due to err: %v", experimentsDetails.AppNS, err)
	}

	rand.Seed(time.Now().Unix())
	randomIndex := rand.Intn(len(podList.Items))
	nodeName := podList.Items[randomIndex].Spec.NodeName

	return nodeName, nil
}

//SetCPUCapacity will fetch the node cpu capacity
func SetCPUCapacity(experimentsDetails *experimentTypes.ExperimentDetails, nodeName string, clients clients.ClientSets) error {
	node, err := clients.KubeClient.CoreV1().Nodes().Get(nodeName, v1.GetOptions{})
	if err != nil {
		return errors.Wrapf(err, "Fail to get the application node, due to ", err)
	}

	cpuCapacity, _ := node.Status.Capacity.Cpu().AsInt64()

	experimentsDetails.NodeCPUcores = int(cpuCapacity)

	return nil
}

// CreateHelperPod derive the attributes for helper pod and create the helper pod
func CreateHelperPod(experimentsDetails *experimentTypes.ExperimentDetails, clients clients.ClientSets, appNodeName string) error {

	helperPod := &apiv1.Pod{
		ObjectMeta: v1.ObjectMeta{
			Name:      experimentsDetails.ExperimentName + "-" + experimentsDetails.RunID,
			Namespace: experimentsDetails.ChaosNamespace,
			Labels: map[string]string{
				"app":      experimentsDetails.ExperimentName,
				"name":     experimentsDetails.ExperimentName + "-" + experimentsDetails.RunID,
				"chaosUID": string(experimentsDetails.ChaosUID),
			},
		},
		Spec: apiv1.PodSpec{
			RestartPolicy: apiv1.RestartPolicyNever,
			NodeName:      appNodeName,
			Containers: []apiv1.Container{
				{
					Name:            experimentsDetails.ExperimentName,
					Image:           experimentsDetails.LIBImage,
					ImagePullPolicy: apiv1.PullAlways,
					Command: []string{
						"/stress-ng",
					},
					Args: []string{
						"--cpu",
						strconv.Itoa(experimentsDetails.NodeCPUcores),
						"--timeout",
						strconv.Itoa(experimentsDetails.ChaosDuration),
					},
				},
			},
		},
	}

	_, err := clients.KubeClient.CoreV1().Pods(experimentsDetails.ChaosNamespace).Create(helperPod)
	return err
}
