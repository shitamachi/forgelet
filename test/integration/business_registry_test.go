//go:build integration

package integration_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

// A TLS endpoint for the literal hostname in the unchanged image workflow.
// All bytes go to the existing authenticated disposable registry. Neither the
// engine nor the job needs access to the real business registry.
func businessRegistryProxy(t *testing.T, ctx context.Context, client *kubernetes.Clientset, image string) string {
	t.Helper()
	if service, err := client.CoreV1().Services("forgelet").Get(ctx, "business-registry", metav1.GetOptions{}); err == nil {
		return service.Spec.ClusterIP
	}
	script := `const fs=require('fs'),http=require('http'),https=require('https');
function proxy(req,res){
 if(req.url==='/healthz'){res.writeHead(200);res.end('ok');return;}
 let path=req.url;
 if(req.headers.host==='ghcr.io'){
  const credential=Buffer.from((req.headers.authorization||'').replace(/^Basic /,''),'base64').toString();
  if(credential!=='forgelet:integration-ghcr-package-token'){res.writeHead(401,{'www-authenticate':'Basic realm="isolated-ghcr"'});res.end();return;}
  req.headers.authorization='Basic '+Buffer.from('test:integration-registry-password').toString('base64');
  if(path.startsWith('/v2/')&&path!=='/v2/')path=path.replace('/v2/','/v2/ghcr-fixture/');
 }
 const upstream=http.request({hostname:'test-registry',port:5000,path:path,method:req.method,headers:{...req.headers,'x-forwarded-proto':'https'}},response=>{
  const headers={...response.headers};
  if(headers.location)headers.location=headers.location.replace('/v2/ghcr-fixture/','/v2/').replace('http://'+req.headers.host,'https://'+req.headers.host);
  res.writeHead(response.statusCode,headers);response.pipe(res);
 });
 upstream.on('error',()=>{res.writeHead(502);res.end('fixture upstream unavailable');});req.pipe(upstream);
}
http.createServer(proxy).listen(8080,'0.0.0.0');
https.createServer({key:fs.readFileSync('/tls/tls.key'),cert:fs.readFileSync('/tls/tls.crt')},proxy).listen(8443,'0.0.0.0');
`
	_, err := client.CoreV1().ConfigMaps("forgelet").Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "business-registry-proxy"}, Data: map[string]string{"proxy.js": script}}, metav1.CreateOptions{})
	must(t, err)
	no := false
	_, err = client.CoreV1().Pods("forgelet").Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "business-registry-proxy", Labels: map[string]string{"app": "business-registry-proxy"}}, Spec: corev1.PodSpec{AutomountServiceAccountToken: &no, Containers: []corev1.Container{{Name: "proxy", Image: image, Command: []string{"/opt/forgelet/node24/bin/node", "/script/proxy.js"}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(8080)}}, PeriodSeconds: 1}, VolumeMounts: []corev1.VolumeMount{{Name: "script", MountPath: "/script", ReadOnly: true}, {Name: "tls", MountPath: "/tls", ReadOnly: true}}}}, Volumes: []corev1.Volume{{Name: "script", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "business-registry-proxy"}}}}, {Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "forgelet-api-tls"}}}}}}, metav1.CreateOptions{})
	must(t, err)
	svc, err := client.CoreV1().Services("forgelet").Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "business-registry"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "business-registry-proxy"}, Ports: []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8080)}, {Name: "https", Port: 443, TargetPort: intstr.FromInt32(8443)}}}}, metav1.CreateOptions{})
	must(t, err)
	waitFor(t, ctx, "business registry TLS proxy ready", func() bool {
		pod, err := client.CoreV1().Pods("forgelet").Get(ctx, "business-registry-proxy", metav1.GetOptions{})
		if err != nil {
			return false
		}
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	})
	return svc.Spec.ClusterIP
}
