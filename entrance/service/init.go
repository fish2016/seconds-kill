package service

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/astaxie/beego/logs"
	"github.com/garyburd/redigo/redis"
	etcd_client "go.etcd.io/etcd/clientv3"
)

var (
	secKillConf *SecSkillConf

	etcdClient *etcd_client.Client
)

func InitService(serviceConf *SecSkillConf) (err error) {
	secKillConf = serviceConf

	err = loadBlackList()
	if err != nil {
		logs.Error("load black list err:%v", err)
		return
	}
	logs.Debug("init service succ, config:%v", secKillConf)

	err = initProxy2LayerRedis()
	if err != nil {
		logs.Error("load proxy2layer redis pool failed, err:%v", err)
		return
	}

	secKillConf.secLimitMgr = &SecLimitMgr{
		UserLimitMap: make(map[int]*Limit, 10000),
		IpLimitMap:   make(map[string]*Limit, 10000),
	}

	secKillConf.SecReqChan = make(chan *SecRequest, secKillConf.SecReqChanSize)
	secKillConf.UserConnMap = make(map[string]chan *SecResult, 10000)

	initRedisProcessFunc()

	return
}

func initRedisProcessFunc() {
	for i := 0; i < secKillConf.WriteProxy2LayerGoroutineNum; i++ {
		go WriteHandle()
	}

	for i := 0; i < secKillConf.ReadProxy2LayerGoroutineNum; i++ {
		go ReadHandle()
	}
}

func initProxy2LayerRedis() (err error) {
	secKillConf.proxy2LayerRedisPool = &redis.Pool{
		MaxIdle:     secKillConf.RedisProxy2LayerConf.RedisMaxIdle,
		MaxActive:   secKillConf.RedisProxy2LayerConf.RedisMaxActive,
		IdleTimeout: time.Duration(secKillConf.RedisProxy2LayerConf.RedisIdleTimeout) * time.Second,
		Dial: func() (redis.Conn, error) {
			return redis.Dial("tcp", secKillConf.RedisProxy2LayerConf.RedisAddr)
		},
	}

	conn := secKillConf.proxy2LayerRedisPool.Get()
	defer conn.Close()

	_, err = conn.Do("ping")
	if err != nil {
		logs.Error("ping redis failed, err:%v", err)
		return
	}

	return
}

func initLayer2ProxyRedis() (err error) {
	secKillConf.layer2ProxyRedisPool = &redis.Pool{
		MaxIdle:     secKillConf.RedisLayer2ProxyConf.RedisMaxIdle,
		MaxActive:   secKillConf.RedisLayer2ProxyConf.RedisMaxActive,
		IdleTimeout: time.Duration(secKillConf.RedisLayer2ProxyConf.RedisIdleTimeout) * time.Second,
		Dial: func() (redis.Conn, error) {
			return redis.Dial("tcp", secKillConf.RedisLayer2ProxyConf.RedisAddr)
		},
	}

	conn := secKillConf.layer2ProxyRedisPool.Get()
	defer conn.Close()

	_, err = conn.Do("ping")
	if err != nil {
		logs.Error("ping redis failed, err:%v", err)
		return
	}

	return
}

func initBlackRedis() (err error) {
	secKillConf.blackRedisPool = &redis.Pool{
		MaxIdle:     secKillConf.RedisBlackConf.RedisMaxIdle,
		MaxActive:   secKillConf.RedisBlackConf.RedisMaxActive,
		IdleTimeout: time.Duration(secKillConf.RedisBlackConf.RedisIdleTimeout) * time.Second,
		Dial: func() (redis.Conn, error) {
			return redis.Dial("tcp", secKillConf.RedisBlackConf.RedisAddr)
		},
	}

	conn := secKillConf.blackRedisPool.Get()
	defer conn.Close()

	_, err = conn.Do("ping")
	if err != nil {
		logs.Error("ping redis failed, err:%v", err)
		return
	}

	return
}

func loadBlackList() (err error) {

	secKillConf.ipBlackMap = make(map[string]bool, 10000)
	secKillConf.idBlackMap = make(map[int]bool, 10000)

	err = initBlackRedis()
	if err != nil {
		logs.Error("init black redis failed, err:%v", err)
		return
	}

	conn := secKillConf.blackRedisPool.Get()
	defer conn.Close()

	reply, err := conn.Do("hgetall", "idblacklist")
	idlist, err := redis.Strings(reply, err)
	if err != nil {
		logs.Warn("hget all failed, err:%v", err)
		return
	}

	for _, v := range idlist {
		id, err := strconv.Atoi(v)
		if err != nil {
			logs.Warn("invalid user id [%v]", id)
			continue
		}
		secKillConf.idBlackMap[id] = true
	}

	reply, err = conn.Do("hgetall", "ipblacklist")
	iplist, err := redis.Strings(reply, err)
	if err != nil {
		logs.Warn("hget all failed, err:%v", err)
		return
	}

	for _, v := range iplist {
		secKillConf.ipBlackMap[v] = true
	}

	go SyncIpBlackList()
	go SyncIdBlackList()
	go SyncSecKillConf()
	return
}

func SyncIpBlackList() {
	var ipList []string
	lastTime := time.Now().Unix()
	for {
		conn := secKillConf.blackRedisPool.Get()
		defer conn.Close()
		reply, err := conn.Do("BLPOP", "blackiplist", time.Second)
		ip, err := redis.String(reply, err)
		if err != nil {
			continue
		}

		curTime := time.Now().Unix()
		ipList = append(ipList, ip)

		if len(ipList) > 100 || curTime-lastTime > 5 {
			secKillConf.RWBlackLock.Lock()
			for _, v := range ipList {
				secKillConf.ipBlackMap[v] = true
			}
			secKillConf.RWBlackLock.Unlock()

			lastTime = curTime
			logs.Info("sync ip list from redis succ, ip[%v]", ipList)
		}
	}
}

func SyncIdBlackList() {
	for {
		conn := secKillConf.blackRedisPool.Get()
		defer conn.Close()
		reply, err := conn.Do("BLPOP", "blackidlist", time.Second)
		id, err := redis.Int(reply, err)
		if err != nil {
			continue
		}

		secKillConf.RWBlackLock.Lock()
		secKillConf.idBlackMap[id] = true
		secKillConf.RWBlackLock.Unlock()

		logs.Info("sync id list from redis succ, ip[%v]", id)
	}
}

func SyncSecKillConf() {
	for {
		cli, err := etcd_client.New(etcd_client.Config{
			Endpoints:   []string{secKillConf.EtcdConf.EtcdAddr},
			DialTimeout: time.Duration(secKillConf.EtcdConf.Timeout) * time.Second,
		})
		if err != nil {
			logs.Error("connect etcd failed, err:", err)
			return
		}

		etcdClient = cli
		err = loadSecConf()
		if err != nil {
			logs.Error("loadSecConf failed, err:", err)
			return
		}
	}

}
func loadSecConf() (err error) {

	resp, err := etcdClient.Get(context.Background(), secKillConf.EtcdConf.EtcdSecProductKey)
	if err != nil {
		logs.Error("get [%s] from etcd failed, err:%v", secKillConf.EtcdConf.EtcdSecProductKey, err)
		return
	}

	var secProductInfo []SecProductInfoConf
	for _, v := range resp.Kvs {
		// logs.Debug("key[%v] valud[%v]", k, v)
		err = json.Unmarshal(v.Value, &secProductInfo)
		if err != nil {
			logs.Error("Unmarshal sec product info failed, err:%v", err)
			return
		}

		// logs.Debug("sec info conf is [%v]", secProductInfo)
	}

	updateSecProductInfo(secProductInfo)
	return
}
func updateSecProductInfo(secProductInfo []SecProductInfoConf) {
	var tmp map[int]*SecProductInfoConf = make(map[int]*SecProductInfoConf, 1024)
	for _, v := range secProductInfo {
		produtInfo := v
		tmp[v.ProductId] = &produtInfo
	}

	secKillConf.RWSecProductLock.Lock()
	secKillConf.SecProductInfoMap = tmp
	secKillConf.RWSecProductLock.Unlock()
}
