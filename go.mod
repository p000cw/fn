module github.com/fnproject/fn

replace cloud.google.com/go => github.com/google/go-cloud v0.4.1-0.20181025204856-f29236cc19de

require (
	github.com/BurntSushi/toml v0.3.1 // indirect
	github.com/agl/ed25519 v0.0.0-20170116200512-5312a6153412 // indirect
	github.com/coreos/go-semver v0.2.1-0.20180108230905-e214231b295a
	github.com/dchest/siphash v1.2.0
	github.com/docker/cli v0.0.0-20190402082642-c89750f836c5
	github.com/docker/distribution v2.7.1+incompatible // indirect
	github.com/docker/docker v0.7.3-0.20190417225312-65e432abe335
	github.com/docker/docker-credential-helpers v0.6.2 // indirect
	github.com/docker/go v1.5.1-1 // indirect
	github.com/docker/go-metrics v0.0.0-20181218153428-b84716841b82 // indirect
	github.com/fnproject/fdk-go v0.0.0-20181025170718-26ed643bea68
	github.com/fsnotify/fsnotify v1.4.7
	github.com/fsouza/go-dockerclient v1.4.0
	github.com/gin-contrib/cors v0.0.0-20170318125340-cf4846e6a636
	github.com/gin-contrib/sse v0.0.0-20170109093832-22d885f9ecc7 // indirect
	github.com/gin-gonic/gin v1.3.0
	github.com/go-sql-driver/mysql v1.4.0
	github.com/golang/groupcache v0.0.0-20180924190550-6f2cf27854a4
	github.com/golang/protobuf v1.3.1
	github.com/grpc-ecosystem/go-grpc-middleware v1.0.0
	github.com/hashicorp/go-version v1.2.0 // indirect
	github.com/inconshreveable/mousetrap v1.0.0 // indirect
	github.com/jmoiron/sqlx v1.2.0
	github.com/json-iterator/go v1.1.5 // indirect
	github.com/leanovate/gopter v0.2.2
	github.com/lib/pq v1.0.0
	github.com/mattn/go-isatty v0.0.4 // indirect
	github.com/mattn/go-sqlite3 v1.9.0
	github.com/miekg/pkcs11 v1.0.2 // indirect
	github.com/moby/moby v1.13.1 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v0.0.0-20180701023420-4b7aa43c6742 // indirect
	github.com/morikuni/aec v0.0.0-20170113033406-39771216ff4c // indirect
	github.com/openzipkin/zipkin-go v0.1.3
	github.com/patrickmn/go-cache v2.1.0+incompatible
	github.com/prometheus/client_golang v0.9.2
	github.com/sirupsen/logrus v1.3.0
	github.com/spf13/cobra v0.0.3 // indirect
	github.com/spf13/pflag v1.0.3 // indirect
	github.com/theupdateframework/notary v0.6.1 // indirect
	github.com/ugorji/go/codec v0.0.0-20181022190402-e5e69e061d4f // indirect
	go.opencensus.io v0.19.0
	golang.org/x/net v0.0.0-20181217023233-e147a9138326
	golang.org/x/sys v0.0.0-20190310054646-10058d7d4faa
	golang.org/x/time v0.0.0-20180412165947-fbb02b2291d2
	google.golang.org/grpc v1.17.0
	gopkg.in/go-playground/assert.v1 v1.2.1 // indirect
	gopkg.in/go-playground/validator.v8 v8.18.2 // indirect
	honnef.co/go/tools v0.0.0-20190418001031-e561f6794a2a
)

replace (
	github.com/Azure/go-ansiterm => ./noop/noop1
	github.com/Microsoft/go-winio => ./noop/noop2
)
