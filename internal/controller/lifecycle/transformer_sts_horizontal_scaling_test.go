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

package lifecycle

import (
	"context"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/apecloud/kubeblocks/internal/controller/graph"
	intctrlutil "github.com/apecloud/kubeblocks/internal/controllerutil"
	"github.com/apecloud/kubeblocks/internal/testutil/apps"
	testutil "github.com/apecloud/kubeblocks/internal/testutil/k8s"
)

var _ = Describe("sts horizontal scaling test", func() {
	When("h-scale with cluster reconcile", func() {
		It("should not delete pvcs generated by h-scale transformer", func() {
			var (
				namespace        = "default"
				clusterDefName   = "sts-h-scale-cluster-def"
				componentDefName = "foo"
				clusterVerName   = "sts-h-scale-cluster-ver"
				clusterName      = "sts-h-scale-cluster"
				componentName    = "bar"
				volumeName       = "data"
				stsName          = clusterName + "-" + componentName
				pvcNameBase      = volumeName + "-" + stsName + "-"
			)
			By("prepare cd, cv, cluster, sts and pvcs")
			cd := apps.NewClusterDefFactory(clusterDefName).
				AddComponentDef(apps.ConsensusMySQLComponent, componentDefName).
				GetObject()
			cv := apps.NewClusterVersionFactory(clusterVerName, cd.Name).
				AddComponent(componentDefName).
				GetObject()
			cluster := apps.NewClusterFactory(namespace, clusterName, cd.Name, cv.Name).
				AddComponent(componentName, componentDefName).
				SetReplicas(3).
				AddVolumeClaimTemplate(volumeName, apps.NewPVCSpec("1G")).
				GetObject()
			template := cluster.Spec.ComponentSpecs[0].ToVolumeClaimTemplates()[0]
			sts := apps.NewStatefulSetFactory(namespace, stsName, cluster.Name, componentName).
				SetReplicas(3).
				AddVolumeClaimTemplate(corev1.PersistentVolumeClaim{ObjectMeta: template.ObjectMeta, Spec: template.Spec}).
				GetObject()
			origSts := sts.DeepCopy()
			pvc1 := apps.NewPersistentVolumeClaimFactory(namespace, pvcNameBase+"1", cluster.Name, componentName, volumeName).
				AddAppInstanceLabel(clusterName).
				GetObject()
			Expect(intctrlutil.SetOwnership(cluster, pvc1, scheme, dbClusterFinalizerName)).Should(Succeed())
			pvc2 := pvc1.DeepCopy()
			pvc2.Name = pvcNameBase + "2"
			Expect(intctrlutil.SetOwnership(cluster, pvc2, scheme, dbClusterFinalizerName)).Should(Succeed())

			By("prepare params for transformer")
			ctrl, k8sMock := testutil.SetupK8sMock()
			defer ctrl.Finish()
			ctx := context.Background()
			transCtx := &ClusterTransformContext{
				Context:     ctx,
				Client:      k8sMock,
				Logger:      log.FromContext(ctx).WithValues("transformer", "h-scale"),
				ClusterDef:  cd,
				ClusterVer:  cv,
				Cluster:     cluster,
				OrigCluster: cluster.DeepCopy(),
			}

			By("prepare initial DAG with sts.action=UPDATE")
			dag := graph.NewDAG()
			rootVertex := &lifecycleVertex{obj: cluster, oriObj: cluster.DeepCopy(), action: actionPtr(STATUS)}
			dag.AddVertex(rootVertex)
			stsVertex := &lifecycleVertex{obj: sts, oriObj: origSts, action: actionPtr(UPDATE)}
			dag.AddVertex(stsVertex)
			dag.Connect(rootVertex, stsVertex)
			By("mock client.List pvcs")
			k8sMock.EXPECT().
				List(gomock.Any(), &corev1.PersistentVolumeClaimList{}, gomock.Any()).
				DoAndReturn(
					func(_ context.Context, list *corev1.PersistentVolumeClaimList, _ ...client.ListOption) error {
						list.Items = []corev1.PersistentVolumeClaim{
							*pvc1,
							*pvc2,
						}
						return nil
					}).AnyTimes()

			transformer := &StsHorizontalScalingTransformer{}

			By("do transform")
			Expect(transformer.Transform(transCtx, dag)).Should(Succeed())
			Expect(len(findAll[*corev1.PersistentVolumeClaim](dag))).Should(Equal(0))

			By("prepare initial DAG with sts.action=DELETE")
			stsVertex.action = actionPtr(DELETE)

			By("do transform")
			Expect(transformer.Transform(transCtx, dag)).Should(Succeed())
			Expect(len(findAll[*corev1.PersistentVolumeClaim](dag))).Should(Equal(2))
		})
	})
})
