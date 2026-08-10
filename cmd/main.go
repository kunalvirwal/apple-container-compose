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

	fmt.Println("listing containers")
	out, err := client.Container.List(context.Background(), container.ListOptions{All: true})
	fmt.Println(out, err)

	fmt.Println("starting container")
	outbool, err := client.Container.Run(context.Background(), "nginx", container.CreateOptions{
		Name:   "my-nginx-container",
		CPUs:   3,
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

	fmt.Println("stopping container")
	out, err = client.Container.Stop(context.Background(), container.StopOptions{
		All:    true,
		Signal: container.SIGTERM,
		IDs:    []string{},
	})
	fmt.Println(out, err)

	fmt.Println("removing container")
	out, err = client.Container.Delete(context.Background(), container.DeleteOptions{
		All:   false,
		IDs:   []string{"my-nginx-container"},
		Force: false,
	})
	fmt.Println(out, err)

	fmt.Println("stopping system")
	out2, err := client.System.Stop(context.Background())
	fmt.Println(out2, err)

}
