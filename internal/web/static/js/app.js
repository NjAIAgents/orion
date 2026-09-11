// The page shell (OR-70): the topbar and nav, and whichever panel the URL
// hash names -- nothing here knows what panels exist.
//
// Class components only, no hooks -- VENDOR.md is explicit about why: the
// hooks build needs a bare "preact" specifier the browser cannot resolve
// without a bundler or an import map, and OR-66 ruled both out.
import { render, Component, h } from "/vendor/preact.module.js";
import htm from "/vendor/htm.module.js";
import { listPanels } from "./panels.js";
import "./panels-registered.js";

const html = htm.bind(h);

// currentName reads the active panel out of location.hash ("#run" -> "run"),
// falling back to the FIRST registered panel when the hash names nothing
// registered -- an empty hash on first load, a typo, or a panel that was
// since removed all read the same way: show the default rather than a
// blank page with no explanation.
//
// ONLY THE FIRST SEGMENT NAMES THE PANEL. "#detail/OR-1/r1" is a parameterised
// route (OR-53's ticket-detail page, reached by clicking a card rather than
// a nav item): the panel name is "detail", and everything after the first
// "/" is that panel's own business to parse from location.hash itself --
// this function's only job is picking which Component renders, never what
// it does with the rest of the hash.
function currentName(panels) {
  const wanted = location.hash.replace(/^#/, "").split("/")[0];
  if (panels.some((p) => p.name === wanted)) return wanted;
  return panels.length > 0 ? panels[0].name : "";
}

// App draws the nav from the registry (listPanels), never from a hardcoded
// list -- the entire point of OR-70's seam. Adding a panel changes what
// this renders with no edit to this file.
class App extends Component {
  constructor() {
    super();
    const panels = listPanels();
    this.state = { panels, current: currentName(panels) };
  }

  componentDidMount() {
    this.onHashChange = () => this.setState({ current: currentName(this.state.panels) });
    window.addEventListener("hashchange", this.onHashChange);
  }

  componentWillUnmount() {
    window.removeEventListener("hashchange", this.onHashChange);
  }

  render(_, { panels, current }) {
    if (panels.length === 0) {
      // No panel module ever imported successfully. A blank nav with a
      // stated reason is diagnosable; a blank page with nothing on it looks
      // like the JS never loaded at all.
      return html`<div class="empty">no panel is registered</div>`;
    }
    const active = panels.find((p) => p.name === current) || panels[0];
    const Panel = active.Component;
    // The sidebar (OR-439) reaches EVERY registered panel, split into two
    // sections on the same inNav flag registerPanel already carries: a page
    // meant to be opened cold ("pages") versus a destination that wants a
    // key/run to be useful ("views" -- OR-53's ticket-detail, OR-437's ask
    // broker). Never a second hardcoded list -- both groups come straight
    // out of listPanels().
    const pagePanels = panels.filter((p) => p.inNav !== false);
    const viewPanels = panels.filter((p) => p.inNav === false);
    const item = (p) => html`
      <a key=${p.name} class="sideitem ${p.name === active.name ? "on" : ""}" href="#${p.name}">
        ${p.title}
      </a>
    `;
    return html`
      <div class="app">
        <div class="sidebar">
          <div class="sidebrand">orion<span class="dot">·</span>web</div>
          <div class="sidenav">
            <div class="sidelabel">pages</div>
            ${pagePanels.map(item)}
            ${viewPanels.length > 0
              ? html`
                  <div class="sidelabel">views</div>
                  ${viewPanels.map(item)}
                `
              : null}
          </div>
        </div>
        <div class="mainarea">
          <${Panel} />
        </div>
      </div>
    `;
  }
}

render(html`<${App} />`, document.getElementById("app"));
