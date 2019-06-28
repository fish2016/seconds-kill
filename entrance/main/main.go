package main

import (
	"fmt"

	_ "github.com/tianxinbaiyun/seconds-kill/entrance/router"

	"github.com/astaxie/beego"
)

func main() {
	fmt.Println("one success")
	err := initConfig()
	if err != nil {
		panic(err)
		return
	}
	fmt.Println("initConfig success")
	err = initSec()
	if err != nil {
		panic(err)
		return
	}

	beego.Run()
}
