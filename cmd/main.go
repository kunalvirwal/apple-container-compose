package main

import (
	"context"
	"fmt"
	"os"

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

	// fmt.Println("starting container")
	// outbool, err := client.Container.Run(context.Background(), "nginx", container.CreateOptions{
	// 	Name:   "my-nginx-container",
	// 	CPUs:   3,
	// 	Memory: "512M",
	// 	Rm:     false,
	// 	Publish: []container.PortMapping{
	// 		{
	// 			HostPort:      8080,
	// 			ContainerPort: 80,
	// 			Protocol:      container.TCP,
	// 		},
	// 	},
	// 	// Arguments: []string{},
	// })
	// fmt.Println(outbool, err)

	// fmt.Println("stopping container")
	// out, err = client.Container.Stop(context.Background(), container.StopOptions{
	// 	All:    true,
	// 	Signal: container.SIGTERM,
	// 	IDs:    []string{},
	// })
	// fmt.Println(out, err)

	// fmt.Println("removing container")
	// out, err = client.Container.Delete(context.Background(), container.DeleteOptions{
	// 	All:   false,
	// 	IDs:   []string{"my-nginx-container"},
	// 	Force: false,
	// })
	// fmt.Println(out, err)

	fmt.Println("building image")
	out, err = client.Images.Build(context.Background(), container.BuildOptions{
		ContextDir: "/Users/kunalvirwal/Desktop/KunalV/shul-data/desktop-shul/KunalV/JS_Codes2/FastChat",
		Tag:        "my-fastchat-image",
		File:       "/Users/kunalvirwal/Desktop/KunalV/shul-data/desktop-shul/KunalV/JS_Codes2/FastChat/Dockerfile",
		OS:         "linux",
		Arch:       []string{"arm64"},
		Pull:       true,
		NoCache:    true,
		// Platform: "linux/arm64",
	}, os.Stdout)
	fmt.Println("build completed with error:", err)
	fmt.Println("build output:", out)

	// fmt.Println("listing images")
	// out, err = client.Images.List(context.Background(), false)
	// fmt.Println(out, err)
	fmt.Println("listing images in quiet mode")
	out, err = client.Images.List(context.Background(), true)
	fmt.Println(out, err)

	// fmt.Println("stopping system")
	// out2, err := client.System.Stop(context.Background())
	// fmt.Println(out2, err)

}
