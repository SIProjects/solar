package solar

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/pkg/errors"
	"github.com/sicashproject/solar/contract"
	"github.com/sicashproject/solar/deployer"
	"github.com/sicashproject/solar/deployer/eth"
	"github.com/sicashproject/solar/deployer/sicash"
	"github.com/sicashproject/solar/varstr"
	kingpin "gopkg.in/alecthomas/kingpin.v2"
)

var (
	app               = kingpin.New("solar", "Solidity smart contract deployment management.")
	sicashRPC           = app.Flag("sicash_rpc", "RPC provider url").Envar("SICASH_RPC").String()
	sicashSenderAddress = app.Flag("sicash_sender", "(sicash) Sender UTXO Address").Envar("SICASH_SENDER").String()

	// geth --rpc --rpcapi="eth,personal,miner"
	ethRPC    = app.Flag("eth_rpc", "RPC provider url").Envar("ETH_RPC").String()
	solarEnv  = app.Flag("env", "Environment name").Envar("SOLAR_ENV").Default("development").String()
	solarRepo = app.Flag("repo", "Path of contracts repository").Envar("SOLAR_REPO").String()
	appTasks  = map[string]func() error{}

	solcOptimize   = app.Flag("optimize", "[solc] should Enable bytecode optimizer").Default("true").Bool()
	solcAllowPaths = app.Flag("allow-paths", "[solc] Allow a given path for imports. A list of paths can be supplied by separating them with a comma.").Default("").String()
)

type RPCPlatform int

const (
	RPCSICash     = iota
	RPCEthereum = iota
)

type solarCLI struct {
	depoyer      deployer.Deployer
	deployerOnce sync.Once

	repo     *contract.ContractsRepository
	repoOnce sync.Once

	reporter     *events
	reporterOnce sync.Once
}

var solar = &solarCLI{}

var (
	errorUnspecifiedRPC = errors.New("Please specify RPC url by setting SICASH_RPC or ETH_RPC or using flag --sicash_rpc or --eth_rpc")
)

func (c *solarCLI) RPCPlatform() RPCPlatform {
	if *sicashRPC == "" && *ethRPC == "" {
		log.Fatalln(errorUnspecifiedRPC)
	}

	if *sicashRPC != "" {
		return RPCSICash
	}

	return RPCEthereum
}

func (c *solarCLI) Reporter() *events {
	c.reporterOnce.Do(func() {
		c.reporter = &events{
			in: make(chan interface{}),
		}

		go c.reporter.Start()
	})

	return c.reporter
}

func (c *solarCLI) SolcOptions() (*CompilerOptions, error) {
	allowPathsStr := *solcAllowPaths
	if allowPathsStr == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, errors.Wrap(err, "solc options")
		}

		allowPathsStr = cwd
	}

	allowPaths := strings.Split(allowPathsStr, ",")

	return &CompilerOptions{
		NoOptimize: !*solcOptimize,
		AllowPaths: allowPaths,
	}, nil
}

// Open the file `solar.{SOLAR_ENV}.json` as contracts repository
func (c *solarCLI) ContractsRepository() *contract.ContractsRepository {
	c.repoOnce.Do(func() {
		var repoFilePath string
		if *solarRepo != "" {
			repoFilePath = *solarRepo
		} else {
			repoFilePath = fmt.Sprintf("solar.%s.json", *solarEnv)
		}

		repo, err := contract.OpenContractsRepository(repoFilePath)
		if err != nil {
			fmt.Printf("error opening contracts repo file %s: %s\n", repoFilePath, err)
			os.Exit(1)
		}

		c.repo = repo
	})

	return c.repo
}

func (c *solarCLI) SICashRPC() *sicash.RPC {
	rpc, err := sicash.NewRPC(*sicashRPC)
	if err != nil {
		fmt.Println("Invalid SICASH RPC URL:", *sicashRPC)
		os.Exit(1)
	}

	return rpc
}

// ExpandJSONParams uses variable substitution syntax (e.g. $Foo, ${Foo}) to as placeholder for contract addresses
func (c *solarCLI) ExpandJSONParams(jsonParams string) string {
	repo := c.ContractsRepository()

	return varstr.Expand(jsonParams, func(key string) string {
		contract, found := repo.Get(key)
		if !found {
			panic(errors.Errorf("Invalid address expansion: %s", key))
		}

		return contract.Address.String()
	})
}

func (c *solarCLI) ConfigureBytesOutputFormat() {
	if *ethRPC != "" {
		contract.SetFormatBytesWithPrefix(true)
	}
}

func (c *solarCLI) Deployer() (deployer deployer.Deployer) {
	log := log.New(os.Stderr, "", log.Lshortfile)

	var err error
	var rpcURL *url.URL

	if rawurl := *sicashRPC; rawurl != "" {

		rpcURL, err = url.ParseRequestURI(rawurl)
		if err != nil {
			log.Fatalf("Invalid RPC url: %#v", rawurl)
		}
		deployer, err = sicash.NewDeployer(rpcURL, c.ContractsRepository(), *sicashSenderAddress)
	}

	if rawurl := *ethRPC; rawurl != "" {
		rpcURL, err = url.ParseRequestURI(rawurl)
		if err != nil {
			log.Fatalf("Invalid RPC url: %#v", rawurl)
		}

		deployer, err = eth.NewDeployer(rpcURL, c.ContractsRepository())
	}

	if deployer == nil {
		log.Fatalln(errorUnspecifiedRPC)
	}

	if err != nil {
		log.Fatalf("NewDeployer error %v", err)
	}

	return deployer
}

func Main() {
	cmdName, err := app.Parse(os.Args[1:])
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	solar.ConfigureBytesOutputFormat()

	task := appTasks[cmdName]
	err = task()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
