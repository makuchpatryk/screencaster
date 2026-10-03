// Behaviour of the fixture pages. Each block runs only on the page that has its element.
const $ = (id) => document.getElementById(id);

if ($("logged-in") && document.cookie.includes("sc_session=fixture")) {
  $("logged-in").hidden = false;
}

if ($("open-form")) {
  $("open-form").addEventListener("click", () => { $("project-form").hidden = false; });
  $("project-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const name = new FormData(e.target).get("name");
    $("toast").textContent = "Created " + name;
    $("toast").hidden = false;
  });
}

if ($("marker")) {
  $("marker").addEventListener("click", () => {
    $("flash").hidden = false;
    setTimeout(() => { $("flash").hidden = true; }, 1000);
  });
}
