DEXD_VERSION = 1
DEXD_SITE = $(BR2_EXTERNAL_AA_PROXY_OS_PATH)/package/dexd/src
DEXD_SITE_METHOD = local
DEXD_GOMOD = dexd
DEXD_BUILD_TARGETS = cmd/dexd
DEXD_LDFLAGS = -s -w

$(eval $(golang-package))
