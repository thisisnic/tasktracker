import "@kenn-io/kit-ui/theme.css";
import "./app.css";
import { mount } from "svelte";
import { initTheme } from "@kenn-io/kit-ui";
import App from "./App.svelte";

initTheme({ storageKey: "tasktracker-theme" });

export default mount(App, { target: document.getElementById("app")! });
