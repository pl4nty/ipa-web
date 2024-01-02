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

	"github.com/gin-gonic/gin"
	"github.com/majd/ipatool/v2/pkg/appstore"
	"howett.net/plist"
)

// TODO proper caching?
type cachedInfo struct {
	cachePath   string
	packageInfo AppleInformation
}

// https://developer.apple.com/documentation/bundleresources/AppleEntitlements
type AppleEntitlements struct {
	AssociatedDomains []string `plist:"com.apple.developer.associated-domains"`
}

// https://developer.apple.com/documentation/bundleresources/information_property_list
type AppleInformation struct {
	CFBundleURLTypes []struct {
		CFBundleURLName    string   `plist:"CFBundleURLName,omitempty"`
		CFBundleTypeRole   string   `plist:"CFBundleTypeRole,omitempty"`
		CFBundleURLSchemes []string `plist:"CFBundleURLSchemes,omitempty"`
	}
}

// TODO https://developer.apple.com/documentation/bundleresources/privacy_manifest_files

func getAccount() (*appstore.Account, error) {
	infoResult, err := dependencies.AppStore.AccountInfo()
	if err != nil {
		if errors.Is(err, appstore.ErrPasswordTokenExpired) {
			loginResult, err := dependencies.AppStore.Login(appstore.LoginInput{Email: os.Getenv("EMAIL"), Password: os.Getenv("PASSWORD")})
			if err != nil {
				return nil, err
			}
			return &loginResult.Account, nil
		}
		return nil, err
	}
	return &infoResult.Account, nil
}

func searchBundle(query string, limit int64) (*appstore.SearchOutput, error) {
	acc, err := getAccount()
	if err != nil {
		return nil, err
	}
	output, err := dependencies.AppStore.Search(appstore.SearchInput{
		Account: *acc,
		Term:    query,
		Limit:   limit,
	})
	if err != nil {
		return nil, err
	}
	return &output, nil
}

func getPackageInfo(bundleID string) (*cachedInfo, error) {
	acc, err := getAccount()
	if err != nil {
		return nil, err
	}

	// download requires app ID
	lookupResult, err := dependencies.AppStore.Lookup(appstore.LookupInput{Account: *acc, BundleID: bundleID})
	if err != nil {
		return nil, err
	}

	cachePath := fmt.Sprintf("%s_%d_%s.plist", lookupResult.App.BundleID, lookupResult.App.ID, lookupResult.App.Version)

	if _, err := os.Stat(cachePath); err == nil {
		cache, err := os.OpenFile(cachePath, os.O_RDONLY, 0644)
		if err != nil {
			return nil, err
		}

		data := new(bytes.Buffer)
		_, err = io.Copy(data, cache)
		if err != nil {
			return nil, err
		}

		var info AppleInformation
		_, err = plist.Unmarshal(data.Bytes(), &info)
		if err != nil {
			return nil, err
		}

		cachedInfo := cachedInfo{
			cachePath:   cachePath,
			packageInfo: info,
		}
		return &cachedInfo, nil
	}

	tmp, err := os.CreateTemp("", "ipa")
	if err != nil {
		return nil, err
	}
	tmp.Close()

	out, err := dependencies.AppStore.Download(appstore.DownloadInput{Account: *acc, App: lookupResult.App, OutputPath: tmp.Name()})
	if err != nil {
		if errors.Is(err, appstore.ErrLicenseRequired) {
			if lookupResult.App.Price == 0 {
				if err := dependencies.AppStore.Purchase(appstore.PurchaseInput{Account: *acc, App: lookupResult.App}); err != nil {
					return nil, err
				}
				out, err = dependencies.AppStore.Download(appstore.DownloadInput{Account: *acc, App: lookupResult.App, OutputPath: tmp.Name()})
				if err != nil {
					return nil, err
				}
			} else {
				return nil, fmt.Errorf("will not purchase non-free app: %w", err)
			}
		} else {
			return nil, err
		}
	}

	var info AppleInformation
	var entitlements AppleEntitlements

	// regexp doesn't support backreferences
	// https://stackoverflow.com/q/23968992
	mainBinary := regexp.MustCompile(`^Payload/(.+)\.app/([^/]+)$`)

	// based on readInfoPlist from https://github.com/majd/ipatool/blob/v2.1.3/pkg/appstore/appstore_replicate_sinf.go
	zipReader, err := zip.OpenReader(out.DestinationPath)
	if err != nil {
		return nil, err
	}
	for _, file := range zipReader.File {
		if strings.Contains(file.Name, ".app/Info.plist") {
			src, err := file.Open()
			if err != nil {
				return nil, err
			}

			data := new(bytes.Buffer)
			_, err = io.Copy(data, src)
			if err != nil {
				return nil, err
			}

			_, err = plist.Unmarshal(data.Bytes(), &info)
			if err != nil {
				return nil, err
			}

			cache, err := os.OpenFile(cachePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, file.Mode())
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

			print(string(lines))

			_, err = plist.Unmarshal(lines, &entitlements)
			if err != nil {
				return nil, err
			}
		}
	}

	if err := os.Remove(out.DestinationPath); err != nil {
		return nil, err
	}

	cachedInfo := cachedInfo{
		cachePath:   cachePath,
		packageInfo: info,
	}
	return &cachedInfo, nil
}

//go:embed static/* templates/*
var content embed.FS

func main() {
	initWithCommand(true, false, "text")
	dependencies.AppStore.Login(appstore.LoginInput{Email: os.Getenv("EMAIL"), Password: os.Getenv("PASSWORD")})
	searchLimit, err := strconv.ParseInt(os.Getenv("SEARCH_LIMIT"), 10, 64)
	if err != nil {
		searchLimit = 15
	}

	// https://github.com/bastomiadi/golang-gin-bootstrap
	r := gin.Default()

	templ := template.Must(template.New("").ParseFS(content, "templates/**/*"))
	r.SetHTMLTemplate(templ)
	r.StaticFS("/public", http.FS(content))

	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "views/index.html", gin.H{})
	})
	r.GET("favicon.ico", func(c *gin.Context) {
		file, _ := content.ReadFile("static/favicon.ico")
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

		info, err := searchBundle(query, searchLimit)
		if err != nil {
			print(err.Error())
			c.String(http.StatusInternalServerError, err.Error())
			return
		}

		c.HTML(http.StatusOK, "views/search.html", gin.H{
			"results": info.Results,
		})
	})
	r.GET("/bundle/:id", func(c *gin.Context) {
		info, err := getPackageInfo(c.Param("id"))
		if err != nil {
			print(err.Error())
			c.String(http.StatusInternalServerError, err.Error())
		} else {
			c.HTML(http.StatusOK, "views/bundle.html", gin.H{
				"id":          c.Param("id"),
				"packageInfo": info.packageInfo,
			})
		}
	})
	r.GET("/download/:id", func(c *gin.Context) {
		info, err := getPackageInfo(c.Param("id"))
		if err != nil {
			print(err.Error())
			c.String(http.StatusInternalServerError, err.Error())
		} else {
			c.File(info.cachePath)
		}
	})

	r.Run()
}
