// Copyright 2020 The SQLFlow Authors. All rights reserved.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package couler

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"

	pb "sqlflow.org/sqlflow/pkg/proto"
	"sqlflow.org/sqlflow/pkg/sql/ir"
)

var defaultDockerImage = "sqlflow/sqlflow"

func getStepEnvs() (map[string]string, error) {
	envs := make(map[string]string)
	envs["SQLFLOW_submitter"] = os.Getenv("SQLFLOW_submitter")
	envs["SQLFLOW_OSS_CHECKPOINT_DIR"] = os.Getenv("SQLFLOW_OSS_CHECKPOINT_DIR")
	for _, env := range os.Environ() {
		pair := strings.SplitN(env, "=", 2)
		if len(pair) != 2 {
			return nil, fmt.Errorf("env: %s should format key=value", env)
		}
		if strings.HasPrefix(pair[0], "SQLFLOW_ALISA_") {
			envs[pair[0]] = pair[1]
		}
	}
	return envs, nil
}

// Run generates Couler program
func Run(programIR ir.SQLProgram, session *pb.Session) (string, error) {
	// TODO(yancey1989): fill session as env
	stepEnvs, err := getStepEnvs()
	if err != nil {
		return "", err
	}
	if session.Submitter != "" {
		stepEnvs["SQLFLOW_submitter"] = session.Submitter
	}
	r := &coulerFiller{
		DataSource: session.DbConnStr,
		StepEnvs:   stepEnvs,
	}
	// NOTE(yancey1989): does not use ModelImage here since the Predict statement
	// does not contain the ModelImage field in SQL Program IR.
	if os.Getenv("SQLFLOW_WORKFLOW_STEP_IMAGE") != "" {
		defaultDockerImage = os.Getenv("SQLFLOW_WORKFLOW_STEP_IMAGE")
	}
	for _, sqlIR := range programIR {
		switch i := sqlIR.(type) {
		case *ir.StandardSQL, *ir.PredictStmt, *ir.ExplainStmt:
			sqlStmt := &sqlStatement{
				OriginalSQL: sqlIR.GetOriginalSQL(), IsExtendedSQL: sqlIR.IsExtended(),
				DockerImage: defaultDockerImage}
			r.SQLStatements = append(r.SQLStatements, sqlStmt)
		case *ir.TrainStmt:
			if r.SQLFlowSubmitter == "katib" {
				sqlStmt, err := ParseKatibSQL(sqlIR.(*ir.TrainStmt))
				if err != nil {
					return "", fmt.Errorf("Fail to parse Katib train statement %s", sqlIR.GetOriginalSQL())
				}
				r.SQLStatements = append(r.SQLStatements, sqlStmt)
			} else {
				sqlStmt := &sqlStatement{
					OriginalSQL: sqlIR.GetOriginalSQL(), IsExtendedSQL: sqlIR.IsExtended(),
					DockerImage: defaultDockerImage}
				r.SQLStatements = append(r.SQLStatements, sqlStmt)
			}
		default:
			return "", fmt.Errorf("unrecognized IR type: %v", i)
		}
	}
	var program bytes.Buffer
	if err := coulerTemplate.Execute(&program, r); err != nil {
		return "", err
	}
	return program.String(), nil
}

func clusterConfigFile() string {
	return os.Getenv("SQLFLOW_COULER_CLUSTER_CONFIG")
}

func writeArgoFile(coulerFileName string) (string, error) {
	argoYaml, err := ioutil.TempFile("/tmp", "sqlflow-argo*.yaml")
	if err != nil {
		return "", fmt.Errorf("cannot create temporary Argo YAML file: %v", err)
	}
	defer argoYaml.Close()

	var cmd *exec.Cmd
	if clusterConfigFile() != "" {
		cmd = exec.Command("couler", "run", "--mode", "argo", "--file", coulerFileName, "--cluster_config", clusterConfigFile())
	} else {
		cmd = exec.Command("couler", "run", "--mode", "argo", "--file", coulerFileName)
	}

	cmd.Env = append(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("generate Argo workflow yaml error: %v", err)
	}
	argoYaml.Write(out)

	return argoYaml.Name(), nil
}

func writeCoulerFile(programIR ir.SQLProgram, session *pb.Session) (string, error) {
	program, err := Run(programIR, session)
	if err != nil {
		return "", fmt.Errorf("generate couler program error: %v", err)
	}

	coulerFile, err := ioutil.TempFile("/tmp", "sqlflow-couler*.py")
	if err != nil {
		return "", fmt.Errorf("write couler program error: %v", err)
	}
	defer coulerFile.Close()
	if _, err := coulerFile.Write([]byte(program)); err != nil {
		return "", err
	}
	return coulerFile.Name(), nil
}

// RunAndWriteArgoFile generates Argo workflow YAML file
func RunAndWriteArgoFile(programIR ir.SQLProgram, session *pb.Session) (string, error) {
	// 1. call codegen_couler.go to generate Couler program.
	coulerFileName, err := writeCoulerFile(programIR, session)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(coulerFileName)

	// 2. compile Couler program into Argo YAML.
	argoFileName, err := writeArgoFile(coulerFileName)
	if err != nil {
		return "", err
	}
	return argoFileName, nil
}
