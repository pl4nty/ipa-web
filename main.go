package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/avast/retry-go/v4"
	"github.com/gin-gonic/gin"
	"github.com/majd/ipatool/v2/pkg/appstore"
	"howett.net/plist"
)

// https://developer.apple.com/documentation/bundleresources/entitlements
type BundleEntitlements struct {
	AssociatedDomains []string `plist:"com.apple.developer.associated-domains"`
}

// https://developer.apple.com/documentation/bundleresources/information_property_list
type BundleInformation struct {
	CFBundleURLTypes []struct {
		CFBundleURLName    string   `plist:"CFBundleURLName,omitempty"`
		CFBundleTypeRole   string   `plist:"CFBundleTypeRole,omitempty"`
		CFBundleURLSchemes []string `plist:"CFBundleURLSchemes,omitempty"`
	}
}

// TODO https://developer.apple.com/documentation/bundleresources/privacy_manifest_files

// TODO Apple Watch subfolder eg Payload/Passbook.app/Watch/PassbookWatchApp.app/

type Bundle struct {
	App          appstore.App
	Information  BundleInformation
	Entitlements BundleEntitlements
}

func login() error {
	_, err := dependencies.AppStore.Login(appstore.LoginInput{Email: os.Getenv("EMAIL"), Password: os.Getenv("PASSWORD")})
	return err
}

func searchBundles(query string, limit int64) (*appstore.SearchOutput, error) {
	accountInfo, err := dependencies.AppStore.AccountInfo()
	if err != nil {
		return nil, err
	}

	output, err := dependencies.AppStore.Search(appstore.SearchInput{
		Account: accountInfo.Account,
		Term:    query,
		Limit:   limit,
	})
	if err != nil {
		return nil, err
	}

	return &output, nil
}

func getBundle(bundleID string) (*Bundle, error) {
	accountInfo, err := dependencies.AppStore.AccountInfo()
	if err != nil {
		return nil, err
	}

	// download requires app ID
	// we could pass as params from search, but lookup is cheap (50-200ms) and avoids browser cache issues
	lookupResult, err := dependencies.AppStore.Lookup(appstore.LookupInput{Account: accountInfo.Account, BundleID: bundleID})
	if err != nil {
		return nil, err
	}

	bundle := Bundle{
		App:          lookupResult.App,
		Information:  BundleInformation{},
		Entitlements: BundleEntitlements{},
	}
	if getCacheFile(bundle.App, &bundle.Information) == nil && getCacheFile(bundle.App, &bundle.Entitlements) == nil {
		return &bundle, nil
	}

	// create temporary filename
	tmp, err := os.CreateTemp("", "ipa")
	if err != nil {
		return nil, err
	}
	tmp.Close()

	// download, 1-time purchase if necessary
	downloadInput := appstore.DownloadInput{Account: accountInfo.Account, App: bundle.App, OutputPath: tmp.Name()}
	ipa, err := dependencies.AppStore.Download(downloadInput)
	if errors.Is(err, appstore.ErrLicenseRequired) {
		err = dependencies.AppStore.Purchase(appstore.PurchaseInput{Account: accountInfo.Account, App: bundle.App})
	}
	if err != nil {
		if strings.Contains(err.Error(), "failed to purchase app") {
			return nil, errors.New("app purchasing is broken due to an Apple API change. please contact tom@tplant.com.au for help, or see this issue for more details: https://github.com/NyaMisty/ipatool-py/issues/58")
		}
		return nil, err
	}
	ipa, err = dependencies.AppStore.Download(downloadInput)
	if err != nil {
		return nil, err
	}

	// regexp doesn't support backreferences
	// https://stackoverflow.com/q/23968992
	mainBinary := regexp.MustCompile(`^Payload/[^/]+\.app/([^/]+)$`)
	infoPlist := regexp.MustCompile(`^Payload/[^/]+\.app/Info\.plist$`)

	// based on readInfoPlist from https://github.com/majd/ipatool/blob/v2.1.3/pkg/appstore/appstore_replicate_sinf.go
	zipReader, err := zip.OpenReader(ipa.DestinationPath)
	if err != nil {
		return nil, err
	}
	for _, file := range zipReader.File {
		if infoPlist.MatchString(file.Name) {
			src, err := file.Open()
			if err != nil {
				return nil, err
			}

			data := new(bytes.Buffer)
			_, err = io.Copy(data, src)
			if err != nil {
				return nil, err
			}

			_, err = plist.Unmarshal(data.Bytes(), &bundle.Information)
			if err != nil {
				return nil, err
			}

			cache, err := os.OpenFile(getCachePath(bundle.App, &bundle.Information), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, file.Mode())
			if err != nil {
				return nil, err
			}

			_, err = io.Copy(cache, data)
			if err != nil {
				return nil, err
			}
		}

		// Package/MyApp.app/MyApp is the main binary, containing the entitlements plist
		matches := mainBinary.FindStringSubmatch(file.Name)
		if len(matches) == 3 && matches[1] == matches[2] {
			src, err := file.Open()
			if err != nil {
				return nil, err
			}

			// carve first newline-delimited plist
			// could be faster by reading only __LINKEDIT section at end of binary
			// backwards read is too complex due to decompression
			scanner := bufio.NewReader(src)

			// would be nicer to use a WriteSeeker with plist.NewDecoder()
			// but stdlib doesn't have an in-memory one
			lines := []byte{}
			for {
				line, err := scanner.ReadSlice('\n')
				if err != nil {
					// use Reader to ignore full buffer, unlike Scanner
					// buffer is much larger than plist line length, longest I've seen is 100 chars
					if err != bufio.ErrBufferFull {
						return nil, err
					}
				}

				if len(lines) == 0 {
					if bytes.HasPrefix(line, []byte("<plist")) {
						lines = append(lines, []byte("<plist version=\"1.0\">")...)
					}
				} else {
					if bytes.HasPrefix(line, []byte("</plist>")) {
						lines = append(lines, []byte("</plist>")...)
						break
					}
					lines = append(lines, line...)
				}
			}

			_, err = plist.Unmarshal(lines, &bundle.Entitlements)
			if err != nil {
				return nil, err
			}

			err = os.WriteFile(getCachePath(bundle.App, &bundle.Entitlements), lines, file.Mode())
			if err != nil {
				return nil, err
			}
		}
	}

	if err := os.Remove(ipa.DestinationPath); err != nil {
		return nil, err
	}

	return &bundle, nil
}

func getCacheFile[P BundleInformation | BundleEntitlements](app appstore.App, p *P) error {
	cache := getCachePath(app, p)

	file, err := os.OpenFile(cache, os.O_RDONLY, 0600)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("cache miss:", cache)
		}
		return err
	}

	data := new(bytes.Buffer)
	_, err = io.Copy(data, file)
	if err != nil {
		return err
	}

	_, err = plist.Unmarshal(data.Bytes(), p)
	if err != nil {
		return err
	}

	return nil
}

func getCachePath[P BundleInformation | BundleEntitlements](app appstore.App, p *P) string {
	path := fmt.Sprintf("cache/%s_%d_%s_%T.plist", app.BundleID, app.ID, app.Version, *p)
	return path
}

var retryOptions = []retry.Option{
	retry.LastErrorOnly(true),
	retry.DelayType(retry.FixedDelay),
	retry.Delay(time.Millisecond),
	retry.Attempts(2),
	retry.RetryIf(func(err error) bool {
		if errors.Is(err, appstore.ErrPasswordTokenExpired) {
			err := login()
			if err == nil {
				return true
			}
			fmt.Println(err.Error())
		}

		return false
	}),
}

func setup() int64 {
	// setup app store client
	initWithCommand(true, false, "text")
	searchLimit, err := strconv.ParseInt(os.Getenv("SEARCH_LIMIT"), 10, 64)
	if err != nil {
		searchLimit = 15
	}
	err = login()
	if err != nil {
		fmt.Println(fmt.Errorf("login failed: %w", err).Error())
	}

	// create cache folder if it doesn't exist
	_, err = os.Stat("cache")
	if errors.Is(err, os.ErrNotExist) {
		err = os.Mkdir("cache", 0700)
		if err != nil {
			fmt.Println(err.Error())
		}
	}

	return searchLimit
}

//go:embed static/* templates/*
var content embed.FS

func main() {
	searchLimit := setup()

	r := gin.Default()
	templ := template.Must(template.New("").ParseFS(content, "templates/**/*"))
	r.SetHTMLTemplate(templ)
	r.StaticFS("/public", http.FS(content))

	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "views/index.html", gin.H{})
	})

	r.GET("favicon.ico", func(c *gin.Context) {
		file, err := content.ReadFile("static/favicon.ico")
		if err != nil {
			fmt.Println(err.Error())
			c.String(http.StatusInternalServerError, err.Error())
			return
		}

		c.Data(
			http.StatusOK,
			"image/x-icon",
			file,
		)
	})

	r.GET("/search", func(c *gin.Context) {
		query := c.Query("q")
		if query == "" {
			c.String(http.StatusBadRequest, "missing search query")
			return
		}

		data, err := retry.DoWithData(func() (*appstore.SearchOutput, error) {
			return searchBundles(query, searchLimit)
		}, retryOptions...)
		if err != nil {
			c.String(http.StatusInternalServerError, err.Error())
			return
		}

		c.HTML(http.StatusOK, "views/search.html", gin.H{
			"Results": data.Results,
		})
	})

	r.GET("/bundle/:id", func(c *gin.Context) {
		data, err := retry.DoWithData(func() (*Bundle, error) {
			return getBundle(c.Param("id"))
		}, retryOptions...)
		if err != nil {
			fmt.Println(err)
			c.String(http.StatusInternalServerError, err.Error())
			return
		}

		domains := []string{}
		for _, domain := range data.Entitlements.AssociatedDomains {
			domains = append(domains, strings.Replace(domain, "applinks:", "", 1))
		}
		data.Entitlements.AssociatedDomains = domains
		c.HTML(http.StatusOK, "views/bundle.html", gin.H{
			"Id":     c.Param("id"),
			"Bundle": data,
		})
	})

	// TODO download Info.plist or Entitlements.plist
	r.GET("/download/:id/:file", func(c *gin.Context) {
		data, err := retry.DoWithData(func() (*Bundle, error) {
			return getBundle(c.Param("id"))
		}, retryOptions...)
		if err != nil {
			c.String(http.StatusInternalServerError, err.Error())
			return
		}

		switch c.Param("file") {
		case "Info.plist":
			c.File(getCachePath(data.App, &BundleInformation{}))
		case "entitlements.plist":
			c.File(getCachePath(data.App, &BundleEntitlements{}))
		default:
			c.String(http.StatusInternalServerError, "unknown file")
		}
	})

	err := r.Run()
	if err != nil {
		fmt.Println(err.Error())
	}
}
