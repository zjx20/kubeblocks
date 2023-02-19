/*
Copyright ApeCloud, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/StudioSol/set"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/util/templates"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/internal/cli/printer"
	"github.com/apecloud/kubeblocks/internal/cli/types"
	"github.com/apecloud/kubeblocks/internal/cli/util"
	cfgcore "github.com/apecloud/kubeblocks/internal/configuration"
)

type reconfigureOptions struct {
	*describeOpsOptions

	clusterName   string
	componentName string
	templateNames []string

	isExplain     bool
	truncEnum     bool
	truncDocument bool
	paramName     string

	keys       []string
	showDetail bool
	// for cache
	tpls []appsv1alpha1.ConfigTemplate
}

type opsRequestDiffOptions struct {
	baseOptions *describeOpsOptions

	clusterName   string
	componentName string
	templateNames []string
	baseVersion   *appsv1alpha1.OpsRequest
	diffVersion   *appsv1alpha1.OpsRequest
}

type parameterTemplate struct {
	name        string
	valueType   string
	miniNum     string
	maxiNum     string
	enum        []string
	description string
	scope       string
}

var (
	describeReconfigureExample = templates.Examples(`
		# describe a cluster, e.g. cluster name is mycluster
		kbcli cluster describe-configure mycluster

		# describe a component, e.g. cluster name is mycluster, component name is mysql
		kbcli cluster describe-configure mycluster --component-name=mysql

		# describe all configuration files. 
		kbcli cluster describe-configure mycluster --component-name=mysql --show-detail

		# describe a content of configuration file. 
		kbcli cluster describe-configure mycluster --component-name=mysql --configure-file=my.cnf --show-detail`)
	explainReconfigureExample = templates.Examples(`
		# describe a cluster, e.g. cluster name is mycluster
		kbcli cluster explain-configure mycluster

		# describe a specified configure template, e.g. cluster name is mycluster
		kbcli cluster explain-configure mycluster --component-name=mysql --template-names=mysql-3node-tpl

		# describe a specified configure template, e.g. cluster name is mycluster
		kbcli cluster explain-configure mycluster --component-name=mysql --template-names=mysql-3node-tpl --trunc-document=false --trunc-enum=false

		# describe a specified parameters, e.g. cluster name is mycluster
		kbcli cluster explain-configure mycluster --component-name=mysql --template-names=mysql-3node-tpl --param=sql_mode`)
	diffConfigureExample = templates.Examples(`
		# compare config files 
		kbcli cluster diff-configure opsrequest1 opsrequest2`)
)

func (r *reconfigureOptions) addCommonFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&r.componentName, "component-name", "", "Specify the name of Component to be describe (e.g. for apecloud-mysql: --component-name=mysql). If the cluster has only one component, unset the parameter.\"")
	cmd.Flags().StringSliceVar(&r.templateNames, "template-names", nil, "Specify the name of the configuration template to be describe. (e.g. for apecloud-mysql: --template-names=mysql-3node-tpl)")
}

func (r *reconfigureOptions) validate() error {
	if r.clusterName == "" {
		return cfgcore.MakeError("missing cluster name")
	}
	if r.componentName == "" {
		return cfgcore.MakeError("missing component name")
	}
	if err := r.syncComponentCfgTpl(); err != nil {
		return err
	}

	if r.isExplain && len(r.templateNames) != 1 {
		return cfgcore.MakeError("explain require one template")
	}

	for _, tplName := range r.templateNames {
		tpl, err := r.findTemplateByName(tplName)
		if err != nil {
			return err
		}
		if r.isExplain && len(tpl.ConfigConstraintRef) == 0 {
			return cfgcore.MakeError("explain command require template has config constraint options")
		}
	}
	return nil
}

func (r *reconfigureOptions) findTemplateByName(tplName string) (*appsv1alpha1.ConfigTemplate, error) {
	if err := r.syncComponentCfgTpl(); err != nil {
		return nil, err
	}

	if tpl := findTplByName(r.tpls, tplName); tpl != nil {
		return tpl, nil
	}
	return nil, cfgcore.MakeError("not found template: %s", tplName)
}

func (r *reconfigureOptions) complete2(args []string) error {
	if len(args) == 0 {
		return makeMissingClusterNameErr()
	}
	r.clusterName = args[0]
	if err := r.complete(args); err != nil {
		return err
	}

	if err := r.syncClusterComponent(); err != nil {
		return err
	}
	if len(r.templateNames) != 0 {
		return nil
	}
	if err := r.syncComponentCfgTpl(); err != nil {
		return err
	}
	if len(r.tpls) == 0 {
		return cfgcore.MakeError("not any config template, not support describe")
	}

	templateNames := make([]string, 0, len(r.tpls))
	if !r.isExplain {
		for _, tpl := range r.tpls {
			templateNames = append(templateNames, tpl.Name)
		}
		r.templateNames = templateNames
		return nil
	}

	// for explain
	for _, tpl := range r.tpls {
		if len(tpl.ConfigConstraintRef) > 0 && len(tpl.ConfigTplRef) > 0 {
			templateNames = append(templateNames, tpl.Name)
		}
	}
	r.templateNames = templateNames
	return nil
}

func (r *reconfigureOptions) syncComponentCfgTpl() error {
	if r.tpls != nil {
		return nil
	}
	tplList, err := util.GetConfigTemplateList(r.clusterName, r.namespace, r.dynamic, r.componentName, false)
	if err != nil {
		return err
	}
	r.tpls = tplList
	return nil
}

func (r *reconfigureOptions) syncClusterComponent() error {
	if r.componentName != "" {
		return nil
	}

	componentNames, err := util.GetComponentsFromClusterCR(client.ObjectKey{
		Namespace: r.namespace,
		Name:      r.clusterName,
	}, r.dynamic)
	if err != nil {
		return makeClusterNotExistErr(r.clusterName)
	}
	if len(componentNames) != 1 {
		return cfgcore.MakeError("when multi component exist, must specify which component to use.")
	}
	r.componentName = componentNames[0]
	return nil
}

func (r *reconfigureOptions) printDescribeReconfigure() error {
	configs, err := r.getReconfigureMeta()
	if err != nil {
		return err
	}

	if r.showDetail {
		r.printConfigureContext(configs)
	}
	printer.PrintComponentConfigMeta(configs, r.clusterName, r.componentName, r.Out)
	return r.printConfigureHistory(configs)
}

func (r *reconfigureOptions) printAllExplainConfigure() error {
	for _, templateName := range r.templateNames {
		fmt.Println("template meta:")
		printer.PrintLineWithTabSeparator(
			printer.NewPair("  TemplateName", templateName),
			printer.NewPair("ComponentName", r.componentName),
			printer.NewPair("ClusterName", r.clusterName),
		)
		if err := r.printExplainConfigure(templateName); err != nil {
			return err
		}
	}
	return nil
}

func (r *reconfigureOptions) printExplainConfigure(tplName string) error {
	tpl, err := r.findTemplateByName(tplName)
	if err != nil {
		return err
	}
	if tpl.ConfigConstraintRef == "" {
		return nil
	}
	configConstraint := appsv1alpha1.ConfigConstraint{}
	if err := util.GetResourceObjectFromGVR(types.ConfigConstraintGVR(), client.ObjectKey{
		Namespace: "",
		Name:      tpl.ConfigConstraintRef,
	}, r.dynamic, &configConstraint); err != nil {
		return err
	}

	confSpec := configConstraint.Spec
	schema := confSpec.ConfigurationSchema.DeepCopy()
	if schema.Schema == nil {
		apiSchema, err := cfgcore.GenerateOpenAPISchema(schema.CUE, "")
		if err != nil {
			return cfgcore.WrapError(err, "failed to generate open api schema")
		}
		schema.Schema = apiSchema
	}
	return r.printConfigConstraint(schema.Schema, set.NewLinkedHashSetString(confSpec.StaticParameters...), set.NewLinkedHashSetString(confSpec.DynamicParameters...))
}

func (r *reconfigureOptions) getReconfigureMeta() (map[appsv1alpha1.ConfigTemplate]*corev1.ConfigMap, error) {
	configs := make(map[appsv1alpha1.ConfigTemplate]*corev1.ConfigMap)
	for _, tplName := range r.templateNames {
		// checked by validate
		tpl, _ := r.findTemplateByName(tplName)
		// fetch config configmap
		cmObj := &corev1.ConfigMap{}
		cmName := cfgcore.GetComponentCfgName(r.clusterName, r.componentName, tpl.VolumeName)
		if err := util.GetResourceObjectFromGVR(types.CMGVR(), client.ObjectKey{
			Name:      cmName,
			Namespace: r.namespace,
		}, r.dynamic, cmObj); err != nil {
			return nil, cfgcore.WrapError(err, "template config instance is not exist, template name: %s, cfg name: %s", tplName, cmName)
		}
		configs[*tpl] = cmObj
	}
	return configs, nil
}

func (r *reconfigureOptions) printConfigureContext(configs map[appsv1alpha1.ConfigTemplate]*corev1.ConfigMap) {
	printer.PrintTitle("Configures Context[${component-name}/${template-name}/${file-name}]")

	keys := set.NewLinkedHashSetString(r.keys...)
	for tpl, cm := range configs {
		for key, context := range cm.Data {
			if keys.Length() != 0 && !keys.InArray(key) {
				continue
			}
			fmt.Fprintf(r.Out, "%s%s\n",
				printer.BoldYellow(fmt.Sprintf("%s/%s/%s:\n", r.componentName, tpl.Name, key)), context)
		}
	}
}

func (r *reconfigureOptions) printConfigureHistory(configs map[appsv1alpha1.ConfigTemplate]*corev1.ConfigMap) error {
	printer.PrintTitle("History modifications")

	// filter reconfigure
	// kubernetes not support fieldSelector with CRD: https://github.com/kubernetes/kubernetes/issues/51046
	listOptions := metav1.ListOptions{
		LabelSelector: strings.Join([]string{types.InstanceLabelKey, r.clusterName}, "="),
	}

	opsList, err := r.dynamic.Resource(types.OpsGVR()).Namespace(r.namespace).List(context.TODO(), listOptions)
	if err != nil {
		return err
	}
	// sort the unstructured objects with the creationTimestamp in positive order
	sort.Sort(unstructuredList(opsList.Items))
	tbl := printer.NewTablePrinter(r.Out)
	tbl.SetHeader("NAME", "CLUSTER", "COMPONENT", "TEMPLATE", "FILES", "STATUS", "POLICY", "PROGRESS", "CREATED-TIME", "VALID-UPDATED")
	for _, obj := range opsList.Items {
		ops := &appsv1alpha1.OpsRequest{}
		if err = runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, ops); err != nil {
			return err
		}
		if ops.Spec.Type != appsv1alpha1.ReconfiguringType {
			continue
		}
		components := getComponentNameFromOps(ops.Spec)
		if !strings.Contains(components, r.componentName) {
			continue
		}
		phase := string(ops.Status.Phase)
		tplNames := getTemplateNameFromOps(ops.Spec)
		keyNames := getKeyNameFromOps(ops.Spec)
		tbl.AddRow(ops.Name,
			ops.Spec.ClusterRef,
			components,
			tplNames,
			keyNames,
			phase,
			getReconfigurePolicy(ops.Status),
			ops.Status.Progress,
			util.TimeFormat(&ops.CreationTimestamp),
			getValidUpdatedParams(ops.Status))
	}
	tbl.Print()
	return nil
}

func (r *reconfigureOptions) hasSpecificParam() bool {
	return len(r.paramName) != 0
}

func (r *reconfigureOptions) isSpecificParam(paramName string) bool {
	return r.paramName == paramName
}

func (r *reconfigureOptions) printConfigConstraint(schema *apiext.JSONSchemaProps,
	staticParameters *set.LinkedHashSetString,
	dynamicParameters *set.LinkedHashSetString) error {
	var (
		index             = 0
		maxDocumentLength = 100
		maxEnumLength     = 20
		spec              = schema.Properties["spec"]
		params            = make([]*parameterTemplate, len(spec.Properties))
	)

	for key, property := range spec.Properties {
		if property.Type == "object" {
			continue
		}
		if r.hasSpecificParam() && !r.isSpecificParam(key) {
			continue
		}

		pt, err := generateParameterTemplate(key, property)
		if err != nil {
			return err
		}
		pt.scope = getScopeType(pt, staticParameters, dynamicParameters)

		if r.hasSpecificParam() {
			printSingleParameterTemplate(pt)
			return nil
		}
		if !r.hasSpecificParam() && r.truncDocument && len(pt.description) > maxDocumentLength {
			pt.description = pt.description[:maxDocumentLength] + "..."
		}
		params[index] = pt
		index++
	}

	if !r.truncEnum {
		maxEnumLength = -1
	}
	printConfigParameterTemplate(params, r.Out, maxEnumLength)
	return nil
}

func (pt *parameterTemplate) enumFormatter(maxFieldLength int) string {
	if len(pt.enum) == 0 {
		return ""
	}
	v := strings.Join(pt.enum, ",")
	if maxFieldLength > 0 && len(v) > maxFieldLength {
		v = v[:maxFieldLength] + "..."
	}
	return v
}

func (pt *parameterTemplate) rangeFormatter() string {
	const (
		r          = "-"
		rangeBegin = "["
		rangeEnd   = "]"
	)

	if len(pt.maxiNum) == 0 && len(pt.miniNum) == 0 {
		return ""
	}

	v := rangeBegin
	if len(pt.miniNum) != 0 {
		v += pt.miniNum
	}
	if len(pt.maxiNum) != 0 {
		v += r
		v += pt.maxiNum
	} else if len(v) != 0 {
		v += r
	}
	v += rangeEnd
	return v
}

func (o *opsRequestDiffOptions) complete(args []string) error {
	isValidReconfigureOps := func(ops *appsv1alpha1.OpsRequest) bool {
		return ops.Spec.Type == appsv1alpha1.ReconfiguringType && ops.Spec.Reconfigure != nil
	}

	if len(args) != 2 {
		return cfgcore.MakeError("missing opsrequest name")
	}

	if err := o.baseOptions.complete(args); err != nil {
		return err
	}

	baseVersion := &appsv1alpha1.OpsRequest{}
	diffVersion := &appsv1alpha1.OpsRequest{}
	if err := util.GetResourceObjectFromGVR(types.OpsGVR(), client.ObjectKey{
		Namespace: o.baseOptions.namespace,
		Name:      args[0],
	}, o.baseOptions.dynamic, baseVersion); err != nil {
		return cfgcore.WrapError(err, "failed to get ops CR [%s]", args[0])
	}
	if err := util.GetResourceObjectFromGVR(types.OpsGVR(), client.ObjectKey{
		Namespace: o.baseOptions.namespace,
		Name:      args[1],
	}, o.baseOptions.dynamic, diffVersion); err != nil {
		return cfgcore.WrapError(err, "failed to get ops CR [%s]", args[1])
	}

	if !isValidReconfigureOps(baseVersion) {
		return cfgcore.MakeError("opsrequest is not valid reconfiguring operation [%s]", client.ObjectKeyFromObject(baseVersion))
	}

	if !isValidReconfigureOps(diffVersion) {
		return cfgcore.MakeError("opsrequest is not valid reconfiguring operation [%s]", client.ObjectKeyFromObject(diffVersion))
	}

	if !o.maybeCompareOps(baseVersion, diffVersion) {
		return cfgcore.MakeError("failed to diff, not same cluster, or same component, or template.")
	}

	o.baseVersion = baseVersion
	o.diffVersion = diffVersion
	return nil
}

func findTemplateStatusByName(status *appsv1alpha1.ReconfiguringStatus, tplName string) *appsv1alpha1.ConfigurationStatus {
	if status == nil {
		return nil
	}

	for i := range status.ConfigurationStatus {
		s := &status.ConfigurationStatus[i]
		if s.Name == tplName {
			return s
		}
	}
	return nil
}

func (o *opsRequestDiffOptions) validate() error {
	var (
		baseStatus = o.baseVersion.Status
		diffStatus = o.diffVersion.Status
	)

	if baseStatus.Phase != appsv1alpha1.SucceedPhase {
		return cfgcore.MakeError("require reconfiguring phase is success!, name: %s, phase: %s", o.baseVersion.Name, baseStatus.Phase)
	}
	if diffStatus.Phase != appsv1alpha1.SucceedPhase {
		return cfgcore.MakeError("require reconfiguring phase is success!, name: %s, phase: %s", o.diffVersion.Name, diffStatus.Phase)
	}

	for _, tplName := range o.templateNames {
		s1 := findTemplateStatusByName(baseStatus.ReconfiguringStatus, tplName)
		s2 := findTemplateStatusByName(diffStatus.ReconfiguringStatus, tplName)
		if s1 == nil || len(s1.LastAppliedConfiguration) == 0 {
			return cfgcore.MakeError("invalid reconfiguring status. CR[%v]", client.ObjectKeyFromObject(o.baseVersion))
		}
		if s2 == nil || len(s2.LastAppliedConfiguration) == 0 {
			return cfgcore.MakeError("invalid reconfiguring status. CR[%v]", client.ObjectKeyFromObject(o.diffVersion))
		}
	}
	return nil
}

func (o *opsRequestDiffOptions) run() error {
	configDiffs := make(map[string][]cfgcore.VisualizedParam, len(o.templateNames))
	for _, tplName := range o.templateNames {
		diff, err := o.diffConfig(tplName)
		if err != nil {
			return err
		}
		configDiffs[tplName] = diff
	}

	printer.PrintTitle("DIFF-CONFIGURE RESULT")
	for tplName, diff := range configDiffs {
		for _, params := range diff {
			printer.PrintLineWithTabSeparator(
				printer.NewPair("  ConfigFile", printer.BoldYellow(params.Key)),
				printer.NewPair("TemplateName", tplName),
				printer.NewPair("ComponentName", o.componentName),
				printer.NewPair("ClusterName", o.clusterName),
				printer.NewPair("UpdateType", string(params.UpdateType)),
			)
			fmt.Fprintf(o.baseOptions.Out, "\n")
			tbl := printer.NewTablePrinter(o.baseOptions.Out)
			tbl.SetHeader("ParameterName", "Value", "Delete")
			for _, v := range params.Parameters {
				tbl.AddRow(v.Key, v.Value, strconv.FormatBool(v.Value == ""))
			}
			tbl.Print()
			fmt.Fprintf(o.baseOptions.Out, "\n\n")
		}
	}
	return nil
}

func (o *opsRequestDiffOptions) maybeCompareOps(base *appsv1alpha1.OpsRequest, diff *appsv1alpha1.OpsRequest) bool {
	getClusterName := func(ops client.Object) string {
		labels := ops.GetLabels()
		if len(labels) == 0 {
			return ""
		}
		return labels[types.InstanceLabelKey]
	}
	getComponentName := func(ops appsv1alpha1.OpsRequestSpec) string {
		return ops.Reconfigure.ComponentName
	}
	getTemplateName := func(ops appsv1alpha1.OpsRequestSpec) []string {
		configs := ops.Reconfigure.Configurations
		names := make([]string, len(configs))
		for i, config := range configs {
			names[i] = config.Name
		}
		return names
	}

	clusterName := getClusterName(base)
	if len(clusterName) == 0 || clusterName != getClusterName(diff) {
		return false
	}
	componentName := getComponentName(base.Spec)
	if len(componentName) == 0 || componentName != getComponentName(diff.Spec) {
		return false
	}
	templateNames := getTemplateName(base.Spec)
	if len(templateNames) == 0 || !reflect.DeepEqual(templateNames, getTemplateName(diff.Spec)) {
		return false
	}

	o.clusterName = clusterName
	o.componentName = componentName
	o.templateNames = templateNames
	return true
}

func (o *opsRequestDiffOptions) diffConfig(tplName string) ([]cfgcore.VisualizedParam, error) {
	var (
		tpl              *appsv1alpha1.ConfigTemplate
		configConstraint = &appsv1alpha1.ConfigConstraint{}
	)

	tplList, err := util.GetConfigTemplateList(o.clusterName, o.baseOptions.namespace, o.baseOptions.dynamic, o.componentName, true)
	if err != nil {
		return nil, err
	}
	if tpl = findTplByName(tplList, tplName); tpl == nil {
		return nil, cfgcore.MakeError("not found template: %s", tplName)
	}
	if err := util.GetResourceObjectFromGVR(types.ConfigConstraintGVR(), client.ObjectKey{
		Namespace: "",
		Name:      tpl.ConfigConstraintRef,
	}, o.baseOptions.dynamic, configConstraint); err != nil {
		return nil, err
	}

	formatCfg := configConstraint.Spec.FormatterConfig
	patchOption := cfgcore.CfgOption{
		Type:    cfgcore.CfgTplType,
		CfgType: formatCfg.Formatter,
		Log:     log.FromContext(context.TODO()),
	}

	base := findTemplateStatusByName(o.baseVersion.Status.ReconfiguringStatus, tplName)
	diff := findTemplateStatusByName(o.diffVersion.Status.ReconfiguringStatus, tplName)

	patch, err := cfgcore.CreateMergePatch(&cfgcore.K8sConfig{
		CfgKey:         client.ObjectKeyFromObject(o.baseVersion),
		Configurations: base.LastAppliedConfiguration,
	}, &cfgcore.K8sConfig{
		CfgKey:         client.ObjectKeyFromObject(o.diffVersion),
		Configurations: diff.LastAppliedConfiguration,
	}, patchOption)
	if err != nil {
		return nil, err
	}

	return cfgcore.GenerateVisualizedParamsList(patch, formatCfg, nil), nil
}

func printSingleParameterTemplate(pt *parameterTemplate) {
	printer.PrintTitle("Configure Constraint")
	// print column "PARAMETER NAME", "RANGE", "ENUM", "SCOPE", "TYPE", "DESCRIPTION"
	printer.PrintPairStringToLine("ParameterName", pt.name)
	printer.PrintPairStringToLine("Range", pt.rangeFormatter())
	printer.PrintPairStringToLine("Enum", pt.enumFormatter(-1))
	printer.PrintPairStringToLine("Scope", pt.scope)
	printer.PrintPairStringToLine("ComponentDefRef", pt.valueType)
	printer.PrintPairStringToLine("Description", pt.description)
}

// printConfigParameterTemplate prints the conditions of resource.
func printConfigParameterTemplate(paramTemplates []*parameterTemplate, out io.Writer, maxFieldLength int) {
	if len(paramTemplates) == 0 {
		return
	}

	sort.SliceStable(paramTemplates, func(i, j int) bool {
		x1 := paramTemplates[i]
		x2 := paramTemplates[j]
		return strings.Compare(x1.name, x2.name) < 0
	})

	tbl := printer.NewTablePrinter(out)
	tbl.SetStyle(printer.TerminalStyle)
	printer.PrintTitle("Configure Constraint")
	tbl.SetHeader("PARAMETER NAME", "RANGE", "ENUM", "SCOPE", "TYPE", "DESCRIPTION")
	for _, pt := range paramTemplates {
		tbl.AddRow(pt.name, pt.rangeFormatter(), pt.enumFormatter(maxFieldLength), pt.scope, pt.valueType, pt.description)
	}
	tbl.Print()
}

func generateParameterTemplate(paramName string, property apiext.JSONSchemaProps) (*parameterTemplate, error) {
	toString := func(v interface{}) (string, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	pt := &parameterTemplate{
		name:        paramName,
		valueType:   property.Type,
		description: strings.TrimSpace(property.Description),
	}
	if property.Minimum != nil {
		b, err := toString(property.Minimum)
		if err != nil {
			return nil, err
		}
		pt.miniNum = b
	}
	if property.Maximum != nil {
		b, err := toString(property.Maximum)
		if err != nil {
			return nil, err
		}
		pt.maxiNum = b
	}
	if property.Enum != nil {
		pt.enum = make([]string, len(property.Enum))
		for i, v := range property.Enum {
			b, err := toString(v)
			if err != nil {
				return nil, err
			}
			pt.enum[i] = b
		}
	}
	return pt, nil
}

func getReconfigurePolicy(status appsv1alpha1.OpsRequestStatus) string {
	if status.ReconfiguringStatus == nil || len(status.ReconfiguringStatus.ConfigurationStatus) == 0 {
		return ""
	}

	var policy string
	reStatus := status.ReconfiguringStatus.ConfigurationStatus[0]
	switch reStatus.UpdatePolicy {
	case appsv1alpha1.AutoReload:
		policy = "reload"
	case appsv1alpha1.NormalPolicy, appsv1alpha1.RestartPolicy, appsv1alpha1.RollingPolicy:
		policy = "restart"
	default:
		return ""
	}
	return printer.BoldYellow(policy)
}

func getValidUpdatedParams(status appsv1alpha1.OpsRequestStatus) string {
	if status.ReconfiguringStatus == nil || len(status.ReconfiguringStatus.ConfigurationStatus) == 0 {
		return ""
	}

	reStatus := status.ReconfiguringStatus.ConfigurationStatus[0]
	if len(reStatus.UpdatedParameters.UpdatedKeys) == 0 {
		return ""
	}
	b, err := json.Marshal(reStatus.UpdatedParameters.UpdatedKeys)
	if err != nil {
		return err.Error()
	}
	return string(b)
}

func findTplByName(tpls []appsv1alpha1.ConfigTemplate, tplName string) *appsv1alpha1.ConfigTemplate {
	for i := range tpls {
		tpl := &tpls[i]
		if tpl.Name == tplName {
			return tpl
		}
	}
	return nil
}

func getScopeType(pt *parameterTemplate, staticParameters *set.LinkedHashSetString, dynamicParameters *set.LinkedHashSetString) string {
	const (
		staticScope  = "static"
		dynamicScope = "dynamic"
	)

	switch {
	case staticParameters.InArray(pt.name):
		return staticScope
	case dynamicParameters.InArray(pt.name):
		return dynamicScope
	case dynamicParameters.Length() == 0 && staticParameters.Length() != 0:
		return dynamicScope
	case dynamicParameters.Length() != 0 && staticParameters.Length() == 0:
		return staticScope
	default:
		return staticScope
	}
}

// NewDescribeReconfigureCmd shows details of history modifications or configuration file of reconfiguring operations
func NewDescribeReconfigureCmd(f cmdutil.Factory, streams genericclioptions.IOStreams) *cobra.Command {
	o := &reconfigureOptions{
		isExplain:          false,
		showDetail:         false,
		describeOpsOptions: newDescribeOpsOptions(f, streams),
	}
	cmd := &cobra.Command{
		Use:               "describe-configure",
		Short:             "Show details of a specific reconfiguring",
		Example:           describeReconfigureExample,
		ValidArgsFunction: util.ResourceNameCompletionFunc(f, types.ClusterGVR()),
		Run: func(cmd *cobra.Command, args []string) {
			util.CheckErr(o.complete2(args))
			util.CheckErr(o.validate())
			util.CheckErr(o.printDescribeReconfigure())
		},
	}
	o.addCommonFlags(cmd)
	cmd.Flags().BoolVar(&o.showDetail, "show-detail", o.showDetail, "If true, the content of the files specified by configure-file will be printed.")
	cmd.Flags().StringSliceVar(&o.keys, "configure-file", nil, "Specify the name of the configuration file to be describe (e.g. for mysql: --configure-file=my.cnf). If unset, all files.")
	return cmd
}

// NewExplainReconfigureCmd shows details of modifiable parameters.
func NewExplainReconfigureCmd(f cmdutil.Factory, streams genericclioptions.IOStreams) *cobra.Command {
	o := &reconfigureOptions{
		isExplain:          true,
		truncEnum:          true,
		truncDocument:      false,
		describeOpsOptions: newDescribeOpsOptions(f, streams),
	}
	cmd := &cobra.Command{
		Use:               "explain-configure",
		Short:             "List the constraint for supported configuration params",
		Example:           explainReconfigureExample,
		ValidArgsFunction: util.ResourceNameCompletionFunc(f, types.ClusterGVR()),
		Run: func(cmd *cobra.Command, args []string) {
			util.CheckErr(o.complete2(args))
			util.CheckErr(o.validate())
			util.CheckErr(o.printAllExplainConfigure())
		},
	}
	o.addCommonFlags(cmd)
	cmd.Flags().BoolVar(&o.truncEnum, "trunc-enum", o.truncEnum, "If the value list length of the parameter is greater than 20, it will be truncated.")
	cmd.Flags().BoolVar(&o.truncDocument, "trunc-document", o.truncDocument, "If the document length of the parameter is greater than 100, it will be truncated.")
	cmd.Flags().StringVar(&o.paramName, "param", o.paramName, "Specify the name of parameter to be query. It clearly display the details of the parameter.")
	return cmd
}

// NewDiffConfigureCmd shows the difference between two configuration version.
func NewDiffConfigureCmd(f cmdutil.Factory, streams genericclioptions.IOStreams) *cobra.Command {
	o := &opsRequestDiffOptions{baseOptions: newDescribeOpsOptions(f, streams)}
	cmd := &cobra.Command{
		Use:               "diff-configure",
		Short:             "List the constraint for supported configuration params",
		Example:           diffConfigureExample,
		ValidArgsFunction: util.ResourceNameCompletionFunc(f, types.ClusterGVR()),
		Run: func(cmd *cobra.Command, args []string) {
			util.CheckErr(o.complete(args))
			util.CheckErr(o.validate())
			util.CheckErr(o.run())
		},
	}
	return cmd
}
