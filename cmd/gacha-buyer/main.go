// gacha-buyer：linux.sb 称号市场自动采购 + 多账号积分归集。
// 全部状态（配置/账号会话/记录）持久化在 SQLite。
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"gacha-buyer/internal/accounts"
	"gacha-buyer/internal/buyer"
	"gacha-buyer/internal/collector"
	"gacha-buyer/internal/config"
	"gacha-buyer/internal/db"
	"gacha-buyer/internal/store"
	"gacha-buyer/internal/web"
)

func main() {
	var (
		dataDir = flag.String("data", "data", "数据目录（SQLite 文件所在）")
		listen  = flag.String("listen", "", "覆盖 Web 监听地址")
	)
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		log.Fatalf("创建数据目录: %v", err)
	}
	dbPath := filepath.Join(*dataDir, "gacha-buyer.db")
	d, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("打开数据库: %v", err)
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		log.Printf("警告：无法收紧数据库权限: %v", err)
	}
	defer d.Close()

	cfg, err := config.Load(d)
	if err != nil {
		log.Fatalf("加载配置: %v", err)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	cfg.Normalize()

	st := store.New(d)

	mgr, err := accounts.New(&cfg, d, log.Printf)
	if err != nil {
		log.Fatalf("初始化账号管理: %v", err)
	}
	mgr.SyncFromConfig()

	eng := buyer.New(&cfg, st, log.Printf)
	eng.Mgr = mgr // 复用主号会话池

	col := collector.New(&cfg, mgr, d, log.Printf)
	srv := web.New(&cfg, d, st, eng, mgr, col)

	go func() {
		log.Printf("Web 控制台: http://%s （数据库: %s）", cfg.Listen, dbPath)
		if err := srv.ListenAndServe(); err != nil {
			fmt.Fprintln(os.Stderr, "Web 服务退出:", strings.TrimSpace(err.Error()))
			os.Exit(1)
		}
	}()

	mgr.StartPatrol() // 每 4 小时巡检全部账号会话
	col.Start()       // 每日归集调度

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	log.Println("收到退出信号，停止后台任务…")
	col.Stop()
	mgr.StopPatrol()
	eng.Stop()
}
