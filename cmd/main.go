package main

import (
	"fmt"

	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

func main() {
	client, err := container.NewClient()
	if err != nil {
		fmt.Println("error creating client:", err)
		return
	}
	_, err = client.System.Status()
	if err == container.ErrSystemNotRunning {
		fmt.Println("system not running, starting system")
		out, err := client.System.Start()
		fmt.Println(out, err)
	}

	out, err := client.Container.List()
	fmt.Println(out, err)

	fmt.Println("stopping system")
	out2, err := client.System.Stop()
	fmt.Println(out2, err)

}
