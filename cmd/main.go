package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Sora233/MiraiGo-Template/config"
	"github.com/alecthomas/kong"
	"github.com/Oumainory/DDBOT-AI"
	"github.com/Oumainory/DDBOT-AI/internal/adminreset"
	"github.com/Oumainory/DDBOT-AI/internal/auth"
	"github.com/Oumainory/DDBOT-AI/internal/platformdb"
	_ "github.com/Oumainory/DDBOT-AI/logging"
	"github.com/Oumainory/DDBOT-AI/lsp"
	_ "github.com/Oumainory/DDBOT-AI/lsp/acfun"
	"github.com/Oumainory/DDBOT-AI/lsp/bilibili"
	localdb "github.com/Oumainory/DDBOT-AI/lsp/buntdb"
	_ "github.com/Oumainory/DDBOT-AI/lsp/douyin"
	_ "github.com/Oumainory/DDBOT-AI/lsp/douyu"
	_ "github.com/Oumainory/DDBOT-AI/lsp/huya"
	"github.com/Oumainory/DDBOT-AI/lsp/permission"
	_ "github.com/Oumainory/DDBOT-AI/lsp/twitch"
	_ "github.com/Oumainory/DDBOT-AI/lsp/twitter"
	_ "github.com/Oumainory/DDBOT-AI/lsp/weibo"
	_ "github.com/Oumainory/DDBOT-AI/lsp/youtube"
	_ "github.com/Oumainory/DDBOT-AI/msg-marker"
	"github.com/Oumainory/DDBOT-AI/utils"
	"github.com/Oumainory/DDBOT-AI/warn"
	"net/http"
	_ "net/http/pprof"
)

type adminCommands struct {
	ResetPassword struct{} `cmd:"reset-password" help:"Reset the local administrator password"`
}

func main() {
	var cli struct {
		// Keep the normal bot invocation as the default path while retaining
		// explicit subcommands such as `admin reset-password`.
		Admin        adminCommands `cmd:"" default:"withargs" help:"Local administrator maintenance"`
		Play         bool          `optional:"" help:"运行play函数，适用于测试和开发"`
		Debug        bool          `optional:"" help:"启动debug模式"`
		Online       bool          `optional:"" help:"跳过等待bot上线，直接启动订阅系统（调试用）"`
		SetAdmin     int64         `optional:"" xor:"c" help:"设置admin权限"`
		Version      bool          `optional:"" xor:"c" short:"v" help:"打印版本信息"`
		SyncBilibili bool          `optional:"" xor:"c" help:"同步b站帐号的关注，适用于更换或迁移b站帐号的时候"`
	}
	ctx := kong.Parse(&cli)
	if ctx.Command() == "admin reset-password" {
		if err := runAdminPasswordReset(); err != nil {
			fmt.Fprintln(os.Stderr, "administrator password reset failed")
			os.Exit(1)
		}
		return
	}

	if cli.Version {
		fmt.Println("Product: DDBOT-AI")
		fmt.Println("Binary: ddbot-ai")
		fmt.Printf("Tags: %v\n", lsp.Tags)
		fmt.Printf("COMMIT_ID: %v\n", lsp.CommitId)
		fmt.Printf("BUILD_TIME: %v\n", lsp.BuildTime)
		os.Exit(0)
	}

	if err := localdb.InitBuntDB(""); err != nil {
		if err == localdb.ErrLockNotHold {
			warn.Warn("tryLock数据库失败：您可能重复启动了这个BOT！\n如果您确认没有重复启动，请删除.lsp.db.lock文件并重新运行。")
		} else {
			warn.Warn("无法正常初始化数据库！请检查.lsp.db文件权限是否正确，如无问题则为数据库文件损坏，请阅读文档获得帮助。")
		}
		return
	}

	// 添加数据库关闭钩子
	utils.AddExitHook(func() {
		localdb.Close()
	})

	// 设置退出钩子处理器
	utils.SetupExitHook()

	if cli.SetAdmin != 0 {
		sm := permission.NewStateManager()
		err := sm.GrantRole(cli.SetAdmin, permission.Admin)
		if err != nil {
			fmt.Printf("设置Admin权限失败 %v\n", err)
		}
		return
	}

	if cli.SyncBilibili {
		config.Init()
		c := bilibili.NewConcern(nil)
		c.StateManager.FreshIndex()
		bilibili.Init()
		c.SyncSub()
		return
	}

	fmt.Println("DDBOT-AI")
	fmt.Println("DDBOT交流群：755612788（已满）、980848391")
	fmt.Println("二次修改:https://github.com/Hoshinonyaruko/DDBOT-ws")
	fmt.Println("三次修改:https://github.com/Oumainory/DDBOT-AI")
	fmt.Println("本分支版本主要以修复功能并接入OneBot协议的BOT框架为目的")
	fmt.Println("主流框架：LLOneBot、NapCat、Lagrange")
	fmt.Println("LLOneBot:https://llonebot.github.io/")
	fmt.Println("NapCat:https://napneko.github.io/")
	fmt.Println("Lagrange:https://lagrangedev.github.io/Lagrange.Doc/")

	if cli.Debug {
		lsp.Debug = true
		go http.ListenAndServe("localhost:6060", nil)
	}

	if cli.Online {
		lsp.SkipOnlineCheck = true
	}

	if cli.Play {
		play()
		return
	}

	DDBOT.SetUpLog()

	DDBOT.Run()
}

func runAdminPasswordReset() error {
	databasePath := strings.TrimSpace(os.Getenv("DDBOT_AI_PLATFORM_DB"))
	if databasePath == "" {
		databasePath = "ddbot-ai.sqlite"
	}
	ctx := context.Background()
	store, err := platformdb.Open(ctx, platformdb.Config{Path: databasePath})
	if err != nil {
		return err
	}
	defer store.Close()

	service := auth.NewService(platformdb.NewAuthRepository(store), auth.Config{})
	return adminreset.Run(ctx, service, os.Stderr, func() (string, error) {
		password, readErr := adminreset.ReadPassword(os.Stdin)
		_, _ = fmt.Fprintln(os.Stderr)
		return string(password), readErr
	})
}
