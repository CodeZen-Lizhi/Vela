import { mount } from "svelte";
import "@fontsource-variable/manrope";
import "@fontsource-variable/newsreader";
import App from "./App.svelte";
import "./app.css";

const target = document.getElementById("app");
if (!target) {
  throw new Error("缺少应用挂载节点 #app");
}

mount(App, { target });
