import { createRouter, createWebHashHistory } from "vue-router";
import Home from "@/views/Home.vue";
import ModelConfig from "@/views/ModelConfig.vue";
import ModelEditor from "@/views/ModelEditor.vue";
import ProviderEditor from "@/views/ProviderEditor.vue";

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    {
      path: "/",
      component: Home,
      meta: { showIcon: true, title: "Cursor助手｜永久免费｜自定义API", directlyClose: false },
    },
    {
      path: "/model-config",
      component: ModelConfig,
      meta: { showIcon: false, title: "模型配置", directlyClose: true },
    },
    {
      path: "/model-editor",
      component: ModelEditor,
      meta: { showIcon: false, title: "模型编辑", directlyClose: true },
    },
    {
      path: "/provider-editor",
      component: ProviderEditor,
      meta: { showIcon: false, title: "中转站配置", directlyClose: true },
    },
  ],
});

export default router;
