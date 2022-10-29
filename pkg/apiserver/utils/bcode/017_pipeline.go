/*
Copyright 2022 The KubeVela Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package bcode

var (
	// ErrContextNotFound means the certain context is not found
	ErrContextNotFound = NewBcode(400, 17001, "pipeline context is not found")
	// ErrContextAlreadyExist means the certain context already exists
	ErrContextAlreadyExist = NewBcode(400, 17002, "pipeline context of pipeline already exist")
	// ErrGetPipelineInfo means failed to get pipeline info
	ErrGetPipelineInfo = NewBcode(400, 17003, "get pipeline info failed")
	// ErrFindingLogPods means no valid pod found
	ErrFindingLogPods = NewBcode(400, 17004, "failed to find log pods")
	// ErrGetPodsLogs means failed to get pods logs
	ErrGetPodsLogs = NewBcode(500, 17006, "failed to get pods logs")
	// ErrReadSourceLog means failed to read source log
	ErrReadSourceLog = NewBcode(500, 17007, "failed to read log from URL source")
	// ErrGetContextBackendData means failed to get context backend data
	ErrGetContextBackendData = NewBcode(500, 17008, "failed to get context backend data")
	// ErrPipelineRunStillRunning means pipeline run is still running
	ErrPipelineRunStillRunning = NewBcode(400, 17009, "pipeline run is still running")
	// ErrPipelineExist means the pipeline is exist
	ErrPipelineExist = NewBcode(400, 17010, "the pipeline is exist")
	// ErrPipelineRunFinished means pipeline run is finished
	ErrPipelineRunFinished = NewBcode(400, 17011, "pipeline run is finished")
)
