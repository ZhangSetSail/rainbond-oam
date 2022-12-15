package export

import (
	"fmt"
	"github.com/goodrain/rainbond-oam/pkg/ram/v1alpha1"
	"github.com/goodrain/rainbond-oam/pkg/util/image"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"os"
	"os/exec"
	"path"
	"sigs.k8s.io/yaml"
	"time"
)

var (
	ImagePush string = "image_push"
	ImageSave string = "image_save"
)

type helmChartExporter struct {
	logger      *logrus.Logger
	ram         v1alpha1.RainbondApplicationConfig
	imageClient image.Client
	mode        string
	homePath    string
	exportPath  string
}

func (h *helmChartExporter) Export() (*Result, error) {
	h.logger.Infof("start export app %s to helm chart spec", h.ram.AppName)
	// Delete the old application group directory and then regenerate the application package
	if err := os.MkdirAll(h.exportPath, 0755); err != nil {
		h.logger.Errorf("prepare export dir failure %s", err.Error())
		return nil, err
	}
	h.logger.Infof("success prepare export dir")
	if imageHandle := h.ram.HelmChart["image_handle"]; imageHandle == ImageSave {
		if err := h.saveComponents(); err != nil {
			return nil, err
		}
		h.logger.Infof("success save components")
		// Save plugin attachments
		if err := h.savePlugins(); err != nil {
			return nil, err
		}
		h.logger.Infof("success save plugins")
	}
	if err := h.initHelmChart(); err != nil {
		return nil, err
	}
	name, err := h.packaging()
	if err != nil {
		return nil, err
	}
	h.logger.Infof("success export app " + h.ram.AppName)
	return &Result{PackagePath: path.Join(h.homePath, name), PackageName: name}, nil
}

func (h *helmChartExporter) saveComponents() error {
	var componentImageNames []string
	for _, component := range h.ram.Components {
		componentName := unicode2zh(component.ServiceCname)
		if component.ShareImage != "" {
			// app is image type
			_, err := h.imageClient.ImagePull(component.ShareImage, component.AppImage.HubUser, component.AppImage.HubPassword, 30)
			if err != nil {
				return err
			}
			h.logger.Infof("pull component %s image success", componentName)
			componentImageNames = append(componentImageNames, component.ShareImage)
		}
	}
	start := time.Now()
	err := h.imageClient.ImageSave(fmt.Sprintf("%s/component-images.tar", h.exportPath), componentImageNames)
	if err != nil {
		logrus.Errorf("Failed to save image(%v) : %s", componentImageNames, err)
		return err
	}
	h.logger.Infof("save component images success, Take %s time", time.Now().Sub(start))
	return nil
}

func (h *helmChartExporter) savePlugins() error {
	var pluginImageNames []string
	for _, plugin := range h.ram.Plugins {
		if plugin.ShareImage != "" {
			// app is image type
			_, err := h.imageClient.ImagePull(plugin.ShareImage, plugin.PluginImage.HubUser, plugin.PluginImage.HubPassword, 30)
			if err != nil {
				return err
			}
			h.logger.Infof("pull plugin %s image success", plugin.PluginName)
			pluginImageNames = append(pluginImageNames, plugin.ShareImage)
		}
	}
	start := time.Now()
	err := h.imageClient.ImageSave(fmt.Sprintf("%s/plugin-images.tar", h.exportPath), pluginImageNames)
	if err != nil {
		logrus.Errorf("Failed to save image(%v) : %s", pluginImageNames, err)
		return err
	}
	h.logger.Infof("save plugin images success, Take %s time", time.Now().Sub(start))
	return nil
}

func (h *helmChartExporter) initHelmChart() error {
	helmChartPath := path.Join(h.exportPath, h.ram.AppName)
	logrus.Infof("路径%v", helmChartPath)
	if err := os.MkdirAll(helmChartPath, 0755); err != nil {
		h.logger.Errorf("prepare export helm chart dir failure %s", err.Error())
		return err
	}
	logrus.Infof("prepare export helm chart dir success")
	err := h.writeChartYaml(helmChartPath)
	if err != nil {
		h.logger.Errorf("%v writeChartYaml failure %v", h.ram.AppName, err)
		return err
	}
	logrus.Infof("writeChartYaml success")
	for i := 0; i < 20; i++ {
		time.Sleep(1 * time.Second)
		if h.CheckValueYamlExist(path.Join(helmChartPath, "values.yaml")) {
			logrus.Infof("value.yaml creeate success")
			break
		}
	}

	err = h.writeTemplateYaml(helmChartPath)
	if err != nil {
		h.logger.Errorf("%v writeValueYaml failure %v", h.ram.AppName, err)
		return err
	}

	return nil
}

type ChartYaml struct {
	ApiVersion  string `json:"apiVersion,omitempty"`
	AppVersion  string `json:"appVersion,omitempty"`
	Description string `json:"description,omitempty"`
	Name        string `json:"name,omitempty"`
	CYType      string `json:"type,omitempty"`
	Version     string `json:"version,omitempty"`
}

func (h *helmChartExporter) writeChartYaml(helmChartPath string) error {
	cy := ChartYaml{
		ApiVersion:  "v2",
		AppVersion:  h.ram.AppVersion,
		Description: h.ram.Annotations["version_info"],
		Name:        h.ram.AppName,
		CYType:      "application",
		Version:     h.ram.AppVersion,
	}
	cyYaml, err := yaml.Marshal(cy)
	if err != nil {
		return err
	}
	err = h.write(path.Join(helmChartPath, "Chart.yaml"), cyYaml)
	if err != nil {
		return err
	}
	return nil
}

func (h *helmChartExporter) CheckValueYamlExist(helmChartPath string) bool {
	_, err := os.Stat(helmChartPath)
	if err != nil {
		return false
	}
	return true
}

func (h *helmChartExporter) writeTemplateYaml(helmChartPath string) error {
	helmChartTemplatePath := path.Join(helmChartPath, "templates")
	if err := os.MkdirAll(helmChartTemplatePath, 0755); err != nil {
		h.logger.Errorf("prepare export helm chart template dir failure %s", err.Error())
		return err
	}
	for _, k8sResource := range h.ram.K8sResources {
		var unstructuredObject unstructured.Unstructured
		err := yaml.Unmarshal([]byte(k8sResource.Content), &unstructuredObject)
		if err != nil {
			return err
		}
		unstructuredObject.SetNamespace("")
		unstructuredObject.SetResourceVersion("")
		unstructuredObject.SetCreationTimestamp(metav1.Time{})
		unstructuredObject.SetUID("")
		unstructuredYaml, err := yaml.Marshal(unstructuredObject)
		if err != nil {
			return err
		}
		err = h.write(path.Join(helmChartTemplatePath, fmt.Sprintf("%v.yaml", unstructuredObject.GetKind())), unstructuredYaml)
		if err != nil {
			return err
		}
	}
	return nil
}

func CheckFileExist(fileName string) bool {
	_, err := os.Stat(fileName)
	if os.IsNotExist(err) {
		return false
	}
	return true
}

func (h *helmChartExporter) write(helmChartFilePath string, meta []byte) error {
	var fl *os.File
	var err error
	if exist := CheckFileExist(helmChartFilePath); exist {
		fl, err = os.OpenFile(helmChartFilePath, os.O_APPEND|os.O_WRONLY, 0755)
		if err != nil {
			return err
		}
	} else {
		fl, err = os.Create(helmChartFilePath)
		if err != nil {
			return err
		}
	}
	defer fl.Close()
	n, err := fl.Write(append(meta, []byte("\n---")...))
	if err != nil {
		return err
	}
	if n < len(append(meta, []byte("\n---")...)) {
		return fmt.Errorf("write insufficient length")
	}
	return nil
}

func (h *helmChartExporter) packaging() (string, error) {
	packageName := fmt.Sprintf("%s-%s-helm.tar.gz", h.ram.AppName, h.ram.AppVersion)
	cmd := exec.Command("tar", "-czf", path.Join(h.homePath, packageName), path.Base(h.exportPath))
	cmd.Dir = h.homePath
	if err := cmd.Run(); err != nil {
		err = fmt.Errorf("Failed to package app %s: %s ", packageName, err.Error())
		h.logger.Error(err)
		return "", err
	}
	return packageName, nil
}
