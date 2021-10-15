// Copyright (c) 2014,2015,2016 Docker, Inc.
// Copyright (c) 2017 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0
//

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/kata-containers/kata-containers/src/runtime/pkg/katautils"
	"github.com/kata-containers/kata-containers/src/runtime/pkg/katautils/katatrace"
	vc "github.com/kata-containers/kata-containers/src/runtime/virtcontainers"
	vcAnnot "github.com/kata-containers/kata-containers/src/runtime/virtcontainers/pkg/annotations"
	"github.com/kata-containers/kata-containers/src/runtime/virtcontainers/pkg/oci"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

var deleteTracingTags = map[string]string{
	"source":    "runtime",
	"package":   "cmd",
	"subsystem": "delete",
}

var deleteCLICommand = cli.Command{
	Name:  "delete",
	Usage: "Delete any resources held by one or more containers",
	ArgsUsage: `<container-id> [container-id...]

   <container-id> is the name for the instance of the container.

EXAMPLE:
   If the container id is "ubuntu01" and ` + katautils.NAME + ` list currently shows the
   status of "ubuntu01" as "stopped" the following will delete resources held
   for "ubuntu01" removing "ubuntu01" from the ` + katautils.NAME + ` list of containers:

       # ` + katautils.NAME + ` delete ubuntu01`,
	Flags: []cli.Flag{
		cli.BoolFlag{
			Name:  "force, f",
			Usage: "Forcibly deletes the container if it is still running (uses SIGKILL)",
		},
	},
	Action: func(context *cli.Context) error {
		ctx, err := cliContextToContext(context)
		if err != nil {
			return err
		}

		args := context.Args()
		if !args.Present() {
			return fmt.Errorf("Missing container ID, should at least provide one")
		}

		force := context.Bool("force")
		for _, cID := range []string(args) {
			if err := delete(ctx, cID, force); err != nil {
				return err
			}
		}

		return nil
	},
}

func delete(ctx context.Context, containerID string, force bool) error {
	span, ctx := katatrace.Trace(ctx, kataLog, "delete", deleteTracingTags)
	defer span.End()

	kataLog = kataLog.WithField("container", containerID)
	setExternalLoggers(ctx, kataLog)
	katatrace.AddTag(span, "container", containerID)

	// Checks the MUST and MUST NOT from OCI runtime specification
	status, sandboxID, err := getExistingContainerInfo(ctx, containerID)
	if err != nil {
		if force {
			kataLog.Warnf("Failed to get container, force will not fail: %s", err)
			return nil
		}
		return err
	}

	containerID = status.ID

	kataLog = kataLog.WithFields(logrus.Fields{
		"container": containerID,
		"sandbox":   sandboxID,
	})

	setExternalLoggers(ctx, kataLog)

	katatrace.AddTag(span, "container", containerID)
	katatrace.AddTag(span, "sandbox", sandboxID)

	containerType, err := oci.GetContainerType(status.Annotations)
	if err != nil {
		return err
	}

	// Retrieve OCI spec configuration.
	ociSpec, err := oci.GetOCIConfig(status)
	if err != nil {
		return err
	}

	forceStop := false
	if oci.StateToOCIState(status.State.State) == oci.StateRunning {
		if !force {
			return fmt.Errorf("Container still running, should be stopped")
		}

		forceStop = true
	}

	switch containerType {
	case vc.PodSandbox:
		if err := deleteSandbox(ctx, sandboxID, force); err != nil {
			return err
		}
	case vc.PodContainer:
		if err := deleteContainer(ctx, sandboxID, containerID, forceStop); err != nil {
			return err
		}
	default:
		return fmt.Errorf("Invalid container type found")
	}

	// Run post-stop OCI hooks.
	if err := katautils.PostStopHooks(ctx, ociSpec, sandboxID, status.Annotations[vcAnnot.BundlePathKey]); err != nil {
		return err
	}

	return katautils.DelContainerIDMapping(ctx, containerID)
}

func deleteSandbox(ctx context.Context, sandboxID string, force bool) error {
	span, ctx := katatrace.Trace(ctx, kataLog, "deleteSandbox", deleteTracingTags)
	defer span.End()

	status, err := vci.StatusSandbox(ctx, sandboxID)
	if err != nil {
		return err
	}

	if oci.StateToOCIState(status.State.State) != oci.StateStopped {
		if _, err := vci.StopSandbox(ctx, sandboxID, force); err != nil {
			return err
		}
	}

	if _, err := vci.DeleteSandbox(ctx, sandboxID); err != nil {
		return err
	}

	return nil
}

func deleteContainer(ctx context.Context, sandboxID, containerID string, forceStop bool) error {
	span, ctx := katatrace.Trace(ctx, kataLog, "deleteContainer", deleteTracingTags)
	defer span.End()

	if forceStop {
		if _, err := vci.StopContainer(ctx, sandboxID, containerID); err != nil {
			return err
		}
	}

	if _, err := vci.DeleteContainer(ctx, sandboxID, containerID); err != nil {
		return err
	}

	return nil
}

func removeCgroupsPath(ctx context.Context, containerID string, cgroupsPathList []string) error {
	span, ctx := katatrace.Trace(ctx, kataLog, "removeCgroupsPath", deleteTracingTags)
	defer span.End()

	if len(cgroupsPathList) == 0 {
		kataLog.WithField("container", containerID).Info("Cgroups files not removed because cgroupsPath was empty")
		return nil
	}

	for _, cgroupsPath := range cgroupsPathList {
		if err := os.RemoveAll(cgroupsPath); err != nil {
			return err
		}
	}

	return nil
}
