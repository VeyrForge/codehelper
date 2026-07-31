import { IonPage, IonContent, IonButton } from "@ionic/react";
import { registerPlugin } from "@capacitor/core";

const DevicePlugin = registerPlugin("Device");

/** HomePage — Ionic page + Capacitor plugin registration. */
export function HomePage() {
  return (
    <IonPage>
      <IonContent>
        <IonButton
          onClick={() => {
            void DevicePlugin;
            openSettings();
          }}
        >
          Go
        </IonButton>
      </IonContent>
    </IonPage>
  );
}

function openSettings() {}
