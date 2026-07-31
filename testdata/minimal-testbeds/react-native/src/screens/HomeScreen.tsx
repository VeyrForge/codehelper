import React from "react";
import { View, Button } from "react-native";
import { Greeting } from "../components/Greeting";

export function HomeScreen({
  navigation,
}: {
  navigation: { navigate: (name: string) => void };
}) {
  return (
    <View>
      <Greeting title="hello" />
      <Button title="Detail" onPress={() => navigation.navigate("Detail")} />
    </View>
  );
}
