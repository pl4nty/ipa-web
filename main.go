package main

import (
	"archive/zip"
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/majd/ipatool/v2/pkg/appstore"
	"howett.net/plist"
)

type cachedInfo struct {
	cachePath   string
	packageInfo packageInfo
}

type packageInfo struct {
	CFBundleURLTypes []struct {
		CFBundleURLName    string   `plist:"CFBundleURLName,omitempty"`
		CFBundleTypeRole   string   `plist:"CFBundleTypeRole,omitempty"`
		CFBundleURLSchemes []string `plist:"CFBundleURLSchemes,omitempty"`
	}
}

func getPackageInfo(bundleID string) (*cachedInfo, error) {
	var acc appstore.Account

	infoResult, err := dependencies.AppStore.AccountInfo()
	if err != nil {
		return nil, err
	}
	acc = infoResult.Account

	// download requires app ID
	lookupResult, err := dependencies.AppStore.Lookup(appstore.LookupInput{Account: acc, BundleID: bundleID})
	if err != nil {
		return nil, err
	}

	cachePath := fmt.Sprintf("%s_%d_%s.plist", lookupResult.App.BundleID, lookupResult.App.ID, lookupResult.App.Version)

	// check if plist is cached
	// TODO abstaction
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
		var info packageInfo
		_, err = plist.Unmarshal(data.Bytes(), &info)
		if err != nil {
			return nil, err
		}

		cachedInfo := cachedInfo{
			cachePath:   cachePath,
			packageInfo: info,
		}
		return &cachedInfo, err
	}

	// hope no-one downloads the same app at the same time
	tmp, err := os.CreateTemp("", "ipa")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	out, err := dependencies.AppStore.Download(appstore.DownloadInput{Account: acc, App: lookupResult.App, OutputPath: tmp.Name()})
	if err != nil {
		return nil, err
	}

	// based on readInfoPlist from https://github.com/majd/ipatool/blob/3199afc494d17495f9f05d019ee97d004fca9248/pkg/appstore/appstore_replicate_sinf.go
	zipReader, err := zip.OpenReader(out.DestinationPath)
	if err != nil {
		return nil, err
	}
	var info packageInfo
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

			break
		}
	}

	if err := os.Remove(out.DestinationPath); err != nil {
		return nil, err
	}

	cachedInfo := cachedInfo{
		cachePath:   cachePath,
		packageInfo: info,
	}
	return &cachedInfo, err
}

//go:embed static/* templates/*
var content embed.FS

func main() {
	initWithCommand(true, false, "text")

	// https://github.com/bastomiadi/golang-gin-bootstrap
	r := gin.Default()

	r.StaticFS("/public", http.FS(content))
	templ := template.Must(template.New("").ParseFS(content, "templates/**/*"))
	r.SetHTMLTemplate(templ)

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
	r.GET("/bundle/:id", func(c *gin.Context) {
		info, err := getPackageInfo(c.Param("id"))
		if err != nil {
			c.String(http.StatusInternalServerError, err.Error())
		}

		c.HTML(http.StatusOK, "views/bundle.html", gin.H{
			"id":          c.Param("id"),
			"packageInfo": info.packageInfo,
		})
	})
	r.GET("/download/:id", func(c *gin.Context) {
		info, err := getPackageInfo(c.Param("id"))
		if err != nil {
			c.String(http.StatusInternalServerError, err.Error())
		}

		c.File(info.cachePath)
	})

	r.Run()
}
