/*
Copyright © 2021 JANOS MIKO <janos.miko@itg.cloud>

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
package main

import (
	"os"
	"os/signal"
	"syscall"

	reward "github.com/rewardenv/reward/internal/core"

	"github.com/rewardenv/reward/cmd"
)

func main() {
	sigs := make(chan os.Signal, 1)

	signal.Notify(
		sigs,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT,
	)

	go func() {
		<-sigs

		err := reward.Cleanup()
		if err != nil {
			os.Exit(1)
		}

		os.Exit(0)
	}()

	cmd.Execute()

	_ = reward.Cleanup()
}
