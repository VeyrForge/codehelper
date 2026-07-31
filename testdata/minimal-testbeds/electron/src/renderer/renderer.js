async function sayHello() {
  const msg = await window.api.greet("renderer");
  document.body.textContent = msg;
}

sayHello();
