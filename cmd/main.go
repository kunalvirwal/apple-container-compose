package main

import (
	"context"
	"fmt"

	"github.com/kunalvirwal/apple-container-compose/pkg/container"
)

func main() {
	client, err := container.NewClient()
	if err != nil {
		fmt.Println("error creating client:", err)
		return
	}
	_, err = client.System.Status(context.Background())
	if err == container.ErrSystemNotRunning {
		fmt.Println("system not running, starting system")
		out, err := client.System.Start(context.Background())
		fmt.Println(out, err)
	}

	out, err := client.Container.List(context.Background(), container.ListOptions{All: true})
	fmt.Println(out, err)

	fmt.Println("starting container")
	outbool, err := client.Container.Run(context.Background(), "nginx", container.CreateOptions{
		Name:   "my-nginx-container",
		CPUs:   2,
		Memory: "512M",
		Rm:     false,
		Publish: []container.PortMapping{
			{
				HostPort:      8080,
				ContainerPort: 80,
				Protocol:      container.TCP,
			},
		},
		// Arguments: []string{},
	})
	fmt.Println(outbool, err)

	fmt.Println("stopping system")
	out2, err := client.System.Stop(context.Background())
	fmt.Println(out2, err)

}
