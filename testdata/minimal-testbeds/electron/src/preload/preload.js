const { contextBridge, ipcRenderer } = require("electron");

function greet(name) {
  return ipcRenderer.invoke("greet", name);
}

contextBridge.exposeInMainWorld("api", {
  greet,
});
