import { IonRouterOutlet } from "@ionic/react";
import { Route } from "react-router-dom";
import { HomePage } from "../pages/HomePage";
import { SettingsPage } from "../pages/SettingsPage";

/** AppRoutes → HomePage / SettingsPage (Ionic Route densify). */
export function AppRoutes() {
  return (
    <IonRouterOutlet>
      <Route exact path="/home" component={HomePage} />
      <Route path="/settings" component={SettingsPage} />
    </IonRouterOutlet>
  );
}
